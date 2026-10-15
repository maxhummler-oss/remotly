// Package agent implements the program that runs on every machine that
// should be remotely controllable. It keeps a WebSocket connection to the
// server, and for each viewer session streams the screen (and audio) over
// WebRTC and injects the viewer's mouse and keyboard input.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/maxhummler-oss/remotly/internal/agent/input"
	"github.com/maxhummler-oss/remotly/internal/agent/stream"
	"github.com/maxhummler-oss/remotly/internal/agent/winsvc"
	"github.com/maxhummler-oss/remotly/internal/protocol"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
)

// Agent is a running agent instance.
type Agent struct {
	cfg     Config
	version string
	service bool
	opts    stream.Options
	audio   *stream.AudioPipeline
	sys     *input.System
	api     *webrtc.API

	pmu       sync.Mutex
	pipelines map[pipeKey]*stream.Pipeline
	encoders  map[stream.Codec]string
	failed    map[stream.Codec]error
	fps       map[stream.Codec]int
	capKbps   int

	wsMu sync.Mutex
	ws   *websocket.Conn
	macs []string

	mu            sync.Mutex
	sessions      map[string]*session
	updating      bool
	lastUpdateTry time.Time
	cancelRun     context.CancelCauseFunc
}

// New probes ffmpeg/encoders, sets up input and the WebRTC stack. service
// is true for the Windows service worker (SYSTEM in the console session).
func New(cfg Config, version string, service bool) (*Agent, error) {
	a := &Agent{
		cfg: cfg, version: version, service: service, sessions: map[string]*session{},
		pipelines: map[pipeKey]*stream.Pipeline{}, encoders: map[stream.Codec]string{},
		failed: map[stream.Codec]error{}, fps: map[stream.Codec]int{},
	}

	opts := cfg.StreamOptions()
	a.opts = opts

	sys, err := input.NewSystem(input.Options{
		Mapping: func() input.Mapping {
			c := a.primary().Capture()
			return input.Mapping{VirtualDesktop: c.VirtualDesktop, Display: c.Display}
		},
		Clipboard: cfg.Clipboard,
		Service:   service,
	})
	if err != nil {
		log.Printf("input disabled (view only): %v", err)
	}
	a.sys = sys

	if service {
		a.opts.Launch = winsvc.DesktopLauncher(func() string {
			if a.sys == nil {
				return ""
			}
			return a.sys.Desktop()
		})
	}
	// H.264 is prepared up front (it is what nearly every viewer uses);
	// other codecs are set up on first demand.
	if _, err := a.pipelineFor(stream.H264, cfg.Display); err != nil {
		log.Printf("H.264 unavailable: %v", err)
		if _, err := a.pipelineFor(stream.VP8, cfg.Display); err != nil {
			return nil, err
		}
	}
	a.audio = stream.NewAudioPipeline(a.opts)

	if a.sys != nil {
		a.sys.SetCallbacks(input.Callbacks{
			Cursor: func(css string) { a.broadcast(map[string]any{"t": "cursor", "c": css}, nil) },
			Clipboard: func(text string) {
				a.broadcast(map[string]any{"t": "clip", "text": text}, func(s *session) bool { return s.policy.ClipToLocal() })
			},
			Desktop: func(name string) {
				// Desktop Duplication cannot follow a desktop switch:
				// restart capture on the new desktop.
				a.pmu.Lock()
				for _, p := range a.pipelines {
					p.Restart()
				}
				a.pmu.Unlock()
				a.broadcast(map[string]any{"t": "desktop", "name": name}, nil)
				a.sendInfo()
			},
		})
	}

	if a.api, err = newAPI(); err != nil {
		return nil, err
	}
	return a, nil
}

type pipeKey struct {
	codec   stream.Codec
	display int
}

// pipelineFor returns (creating on first use) the pipeline for a codec and
// display. Viewers watching the same display share one capture process.
func (a *Agent) pipelineFor(codec stream.Codec, display int) (*stream.Pipeline, error) {
	a.pmu.Lock()
	defer a.pmu.Unlock()
	key := pipeKey{codec, display}
	if p := a.pipelines[key]; p != nil {
		return p, nil
	}
	if err := a.failed[codec]; err != nil {
		return nil, err
	}
	opts := a.opts
	opts.Display = display
	enc, ok := a.encoders[codec]
	if !ok {
		probe := opts
		if probe.FPS == 0 {
			probe.FPS = 60
		}
		var err error
		if enc, err = stream.SelectEncoder(probe, codec, log.Printf); err != nil {
			a.failed[codec] = err
			return nil, err
		}
		a.encoders[codec] = enc
		fps := a.opts.FPS
		if fps == 0 {
			fps = 60
			if runtime.GOOS == "windows" && stream.IsHardware(enc) {
				// Capturing at 120 fps halves the time a screen update
				// waits to be grabbed, even on a 60 Hz display; hardware
				// encoders handle the (mostly duplicate) frames for free.
				fps = 120
			}
		}
		a.fps[codec] = fps
	}
	opts.FPS = a.fps[codec]
	log.Printf("%s pipeline for display %d: %s at %d fps, %d kbit/s", codec, display, enc, opts.FPS, opts.BitrateKbps)
	p := stream.NewPipeline(opts, enc)
	p.SetCap(a.capKbps)
	a.pipelines[key] = p
	return p, nil
}

// primary returns the main pipeline (H.264 if available).
func (a *Agent) primary() *stream.Pipeline {
	a.pmu.Lock()
	defer a.pmu.Unlock()
	if p := a.pipelines[pipeKey{stream.H264, a.cfg.Display}]; p != nil {
		return p
	}
	for _, p := range a.pipelines {
		return p
	}
	return nil
}

// displays returns how many displays a viewer can switch between. With
// whole-desktop capture (gdigrab) there is only one "display".
func (a *Agent) displays() int {
	p := a.primary()
	if a.sys == nil || p == nil {
		return 1
	}
	switch p.Capture().Method {
	case "ddagrab", "avfoundation":
		return max(1, a.sys.Displays())
	}
	return 1
}

// setCap applies the server's bitrate cap to all pipelines.
func (a *Agent) setCap(kbps int) {
	a.pmu.Lock()
	defer a.pmu.Unlock()
	a.capKbps = kbps
	for _, p := range a.pipelines {
		p.SetCap(kbps)
	}
}

// h264Fmtp advertises Constrained Baseline, which every browser decodes
// (with hardware acceleration where available).
const h264Fmtp = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"

func newAPI() (*webrtc.API, error) {
	m := &webrtc.MediaEngine{}
	feedback := []webrtc.RTCPFeedback{
		{Type: "goog-remb"},
		{Type: "ccm", Parameter: "fir"},
		{Type: "nack"},
		{Type: "nack", Parameter: "pli"},
	}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: h264Fmtp, RTCPFeedback: feedback,
		},
		PayloadType: 102,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000, RTCPFeedback: feedback},
		PayloadType:        96,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: opusFmtp,
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}
	if err := m.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: playoutDelayURI}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	ir := &interceptor.Registry{}
	// NACK-based retransmission and RTCP reports.
	if err := webrtc.RegisterDefaultInterceptors(m, ir); err != nil {
		return nil, err
	}
	ir.Add(playoutDelayFactory{})
	se := webrtc.SettingEngine{}
	// Enough send buffer for keyframe bursts at high bitrates.
	se.SetSCTPMaxReceiveBufferSize(4 << 20)
	return webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(ir), webrtc.WithSettingEngine(se)), nil
}

const opusFmtp = "minptime=10;useinbandfec=1;stereo=1;sprop-stereo=1"

// Run connects to the server and serves sessions until ctx is cancelled,
// reconnecting with backoff whenever the connection drops.
func (a *Agent) Run(parent context.Context) error {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	a.cancelRun = cancel
	backoff := time.Second
	for {
		started := time.Now()
		err := a.connectOnce(ctx)
		a.closeAllSessions("server connection lost")
		if ctx.Err() != nil {
			if errors.Is(context.Cause(ctx), ErrUpdated) {
				return ErrUpdated
			}
			return nil
		}
		var fatal *fatalError
		if errors.As(err, &fatal) {
			return fatal
		}
		if time.Since(started) > 30*time.Second {
			backoff = time.Second
		}
		log.Printf("server connection: %v (retrying in %v)", err, backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

type fatalError struct{ msg string }

func (e *fatalError) Error() string { return e.msg }

func wsURL(server string) (string, error) {
	u, err := url.Parse(strings.TrimRight(server, "/"))
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("invalid server URL %q (expected https://host)", server)
	}
	u.Path += "/ws/agent"
	return u.String(), nil
}

func (a *Agent) info() *protocol.AgentInfo {
	host, _ := os.Hostname()
	info := &protocol.AgentInfo{
		Hostname: host,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Version:  a.version,
		Encoder:  a.primary().Encoder(),
		Capture:  a.primary().Variant(),
		Audio:    a.audio != nil,
		MACs:     a.macs,
	}
	if a.sys != nil {
		info.Desktop = a.sys.Desktop()
	}
	return info
}

// sendInfo re-sends the agent info (e.g. after a desktop switch).
func (a *Agent) sendInfo() {
	a.send(protocol.Message{Type: protocol.TypeHello, Agent: a.info()})
}

func (a *Agent) connectOnce(ctx context.Context) error {
	target, err := wsURL(a.cfg.Server)
	if err != nil {
		return &fatalError{err.Error()}
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		Proxy:            http.ProxyFromEnvironment,
		TLSClientConfig:  TLSConfig(a.cfg.CAFingerprint, a.cfg.Server),
	}
	conn, resp, err := dialer.DialContext(ctx, target, nil)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
			return errors.New("server is rate limiting this IP")
		}
		return err
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	a.macs = localMACs(conn.LocalAddr())

	hello := protocol.Message{Type: protocol.TypeHello, DeviceID: a.cfg.DeviceID, Secret: a.cfg.Secret, Agent: a.info()}
	if err := conn.WriteJSON(hello); err != nil {
		return err
	}
	var welcome protocol.Message
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	if err := conn.ReadJSON(&welcome); err != nil {
		return err
	}
	if welcome.Type == protocol.TypeError {
		if welcome.Reason == "unauthorized" {
			return errors.New("server rejected the device credentials (device revoked? re-run enroll)")
		}
		return errors.New(welcome.Reason)
	}
	a.setCap(welcome.MaxBitrateKbps)
	go a.maybeUpdate(ctx, welcome.UpdateFile, welcome.UpdateSHA256)
	conn.SetPingHandler(func(data string) error {
		a.wsMu.Lock()
		defer a.wsMu.Unlock()
		return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(5*time.Second))
	})

	a.wsMu.Lock()
	a.ws = conn
	a.wsMu.Unlock()
	log.Printf("connected to %s (%s)", a.cfg.Server, welcome.ServerName)

	for {
		// The server pings every 20s.
		conn.SetReadDeadline(time.Now().Add(70 * time.Second))
		var msg protocol.Message
		if err := conn.ReadJSON(&msg); err != nil {
			return err
		}
		a.handle(msg)
	}
}

// localMACs returns the MAC of the interface used to reach the server
// (for Wake-on-LAN), followed by other physical interfaces.
func localMACs(local net.Addr) []string {
	var ip net.IP
	if tcp, ok := local.(*net.TCPAddr); ok {
		ip = tcp.IP
	}
	ifaces, _ := net.Interfaces()
	var first, rest []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || len(iface.HardwareAddr) != 6 {
			continue
		}
		mac := iface.HardwareAddr.String()
		addrs, _ := iface.Addrs()
		match := false
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ip != nil && ipn.IP.Equal(ip) {
				match = true
			}
		}
		if match {
			first = append(first, mac)
		} else if iface.Flags&net.FlagUp != 0 {
			rest = append(rest, mac)
		}
	}
	return append(first, rest...)
}

func (a *Agent) send(msg protocol.Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	a.wsMu.Lock()
	defer a.wsMu.Unlock()
	if a.ws == nil {
		return
	}
	a.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := a.ws.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Printf("signaling write: %v", err)
	}
}

func (a *Agent) handle(msg protocol.Message) {
	switch msg.Type {
	case protocol.TypeConnect:
		if err := a.startSession(msg); err != nil {
			log.Printf("session %s: %v", msg.Session, err)
			a.send(protocol.Message{Type: protocol.TypeEnd, Session: msg.Session, Reason: err.Error()})
		}
	case protocol.TypeAnswer:
		if s := a.session(msg.Session); s != nil {
			if err := s.setAnswer(msg.SDP); err != nil {
				s.close("bad answer: "+err.Error(), true)
			}
		}
	case protocol.TypeCandidate:
		if s := a.session(msg.Session); s != nil {
			s.addCandidate(msg.Candidate)
		}
	case protocol.TypeEnd:
		if s := a.session(msg.Session); s != nil {
			s.close(msg.Reason, false)
		}
	}
}

func (a *Agent) session(id string) *session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[id]
}

// broadcast sends a control message to every connected viewer that
// passes the filter (nil = all).
func (a *Agent) broadcast(v any, filter func(*session) bool) {
	data, _ := json.Marshal(v)
	a.mu.Lock()
	list := make([]*session, 0, len(a.sessions))
	for _, s := range a.sessions {
		if filter == nil || filter(s) {
			list = append(list, s)
		}
	}
	a.mu.Unlock()
	for _, s := range list {
		s.sendControl(data)
	}
}

func (a *Agent) closeAllSessions(reason string) {
	a.mu.Lock()
	list := make([]*session, 0, len(a.sessions))
	for _, s := range a.sessions {
		list = append(list, s)
	}
	a.mu.Unlock()
	for _, s := range list {
		s.close(reason, false)
	}
	a.wsMu.Lock()
	a.ws = nil
	a.wsMu.Unlock()
}
