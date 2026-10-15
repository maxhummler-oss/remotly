package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/maxhummler-oss/remotly/internal/agent/input"
	"github.com/maxhummler-oss/remotly/internal/agent/stream"
	"github.com/maxhummler-oss/remotly/internal/protocol"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// session is one viewer connected to this agent.
type session struct {
	id       string
	viewer   string
	agent    *Agent
	pipeline *stream.Pipeline
	policy   *protocol.Policy
	pc       *webrtc.PeerConnection
	video    *webrtc.TrackLocalStaticSample
	audio    *webrtc.TrackLocalStaticSample
	inj      *input.Injector

	mu         sync.Mutex
	control    *webrtc.DataChannel
	subscribed bool
	closed     bool
}

// current returns the pipeline the session is watching (it changes when
// the viewer switches displays).
func (s *session) current() *stream.Pipeline {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pipeline
}

// chooseCodec picks the best codec the viewer can decode.
func (a *Agent) chooseCodec(viewer []string, display int) (*stream.Pipeline, error) {
	supports := func(mime string) bool {
		if len(viewer) == 0 {
			return mime == webrtc.MimeTypeH264 // old viewers did not report codecs
		}
		for _, c := range viewer {
			if strings.EqualFold(c, mime) {
				return true
			}
		}
		return false
	}
	var errs []string
	for _, c := range []struct {
		codec stream.Codec
		mime  string
	}{{stream.H264, webrtc.MimeTypeH264}, {stream.VP8, webrtc.MimeTypeVP8}} {
		if !supports(c.mime) {
			continue
		}
		p, err := a.pipelineFor(c.codec, display)
		if err == nil {
			return p, nil
		}
		errs = append(errs, err.Error())
	}
	if len(errs) == 0 {
		return nil, errors.New("der Browser unterstützt weder H.264 noch VP8")
	}
	return nil, errors.New(strings.Join(errs, "; "))
}

func (a *Agent) startSession(msg protocol.Message) error {
	pipeline, err := a.chooseCodec(msg.Codecs, a.cfg.Display)
	if err != nil {
		return err
	}
	ice := make([]webrtc.ICEServer, 0, len(msg.ICEServers))
	for _, s := range msg.ICEServers {
		ice = append(ice, webrtc.ICEServer{URLs: s.URLs, Username: s.Username, Credential: s.Credential})
	}
	cfg := webrtc.Configuration{ICEServers: ice}
	if msg.ForceRelay {
		cfg.ICETransportPolicy = webrtc.ICETransportPolicyRelay
	}
	pc, err := a.api.NewPeerConnection(cfg)
	if err != nil {
		return err
	}
	policy := msg.Policy
	if policy == nil {
		policy = &protocol.Policy{Clipboard: "both"}
	}
	if !a.cfg.Clipboard {
		policy.Clipboard = "off" // the machine owner disabled it locally
	}
	s := &session{id: msg.Session, viewer: msg.Viewer, agent: a, pc: pc, pipeline: pipeline, policy: policy}
	fail := func(err error) error {
		pc.Close()
		return err
	}

	videoCap := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: h264Fmtp}
	if pipeline.Codec() == stream.VP8 {
		videoCap = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}
	}
	// Video and audio use separate stream IDs on purpose: the browser then
	// does not delay video to lip-sync it with the (larger) audio jitter
	// buffer. For remote desktop, immediate video beats perfect A/V sync.
	s.video, err = webrtc.NewTrackLocalStaticSample(videoCap, "video", "remotly-video")
	if err != nil {
		return fail(err)
	}
	videoSender, err := pc.AddTrack(s.video)
	if err != nil {
		return fail(err)
	}
	// RTCP must be read for NACK/receiver reports to be processed; a
	// PLI/FIR means the viewer lost data it cannot recover from.
	go func() {
		for {
			pkts, _, err := videoSender.ReadRTCP()
			if err != nil {
				return
			}
			for _, p := range pkts {
				switch p := p.(type) {
				case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
					s.current().RequestKeyframe()
				case *rtcp.ReceiverReport:
					for _, r := range p.Reports {
						s.current().ReportLoss(float64(r.FractionLost) / 256)
					}
				}
			}
		}
	}()

	if a.audio != nil {
		s.audio, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: opusFmtp,
		}, "audio", "remotly-audio")
		if err != nil {
			return fail(err)
		}
		audioSender, err := pc.AddTrack(s.audio)
		if err != nil {
			return fail(err)
		}
		go func() {
			buf := make([]byte, 1500)
			for {
				if _, _, err := audioSender.Read(buf); err != nil {
					return
				}
			}
		}()
	}

	if a.sys != nil {
		s.inj = a.sys.NewInjector(func() input.Mapping {
			c := s.current().Capture()
			return input.Mapping{VirtualDesktop: c.VirtualDesktop, Display: c.Display}
		})
	}

	// Keys, clicks and control messages must arrive reliably and in order;
	// pointer motion is sent unreliably so a lost packet never delays newer
	// positions behind a retransmission.
	ordered := false
	var zero uint16
	controlDC, err := pc.CreateDataChannel("input", nil)
	if err != nil {
		return fail(err)
	}
	motionDC, err := pc.CreateDataChannel("motion", &webrtc.DataChannelInit{Ordered: &ordered, MaxRetransmits: &zero})
	if err != nil {
		return fail(err)
	}
	controlDC.OnOpen(func() {
		s.mu.Lock()
		s.control = controlDC
		s.mu.Unlock()
		info := map[string]any{
			"t":         "info",
			"name":      a.info().Hostname,
			"os":        runtime.GOOS,
			"encoder":   pipeline.Encoder(),
			"codec":     pipeline.Codec(),
			"capture":   pipeline.Variant(),
			"fps":       pipeline.FPS(),
			"display":   pipeline.Display(),
			"displays":  a.displays(),
			"bitrate":   a.cfg.BitrateKbps,
			"input":     s.inj != nil && !policy.ViewOnly,
			"viewOnly":  policy.ViewOnly,
			"clipLocal": policy.ClipToLocal(),
			"audio":     a.audio != nil,
			"clipboard": policy.ClipToRemote(),
			"cursor":    "stream",
		}
		if a.cfg.UseLocalCursor() {
			info["cursor"] = "local"
		}
		if a.sys != nil {
			info["desktop"] = a.sys.Desktop()
			if c := a.sys.Cursor(); c != "" {
				info["shape"] = c
			}
		}
		data, _ := json.Marshal(info)
		controlDC.SendText(string(data))
	})
	controlDC.OnMessage(func(m webrtc.DataChannelMessage) { s.onInput(controlDC, m.Data) })
	motionDC.OnMessage(func(m webrtc.DataChannelMessage) { s.onInput(motionDC, m.Data) })

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		data, _ := json.Marshal(c.ToJSON())
		a.send(protocol.Message{Type: protocol.TypeCandidate, Session: s.id, Candidate: data})
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("session %s: %s", s.id, state)
		switch state {
		case webrtc.PeerConnectionStateConnected:
			s.logRoute()
			s.subscribe()
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			go s.close("connection "+state.String(), true)
		}
	})

	a.mu.Lock()
	a.sessions[s.id] = s
	a.mu.Unlock()

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		s.close(err.Error(), false)
		return err
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		s.close(err.Error(), false)
		return err
	}
	a.send(protocol.Message{Type: protocol.TypeOffer, Session: s.id, SDP: offer.SDP})
	log.Printf("session %s: %s connecting", s.id, s.viewer)

	// Give up if the viewer never completes the handshake.
	time.AfterFunc(30*time.Second, func() {
		if pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
			s.close("connection timeout", true)
		}
	})
	return nil
}

func (s *session) setAnswer(sdp string) error {
	return s.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp})
}

func (s *session) addCandidate(raw json.RawMessage) {
	var c webrtc.ICECandidateInit
	if err := json.Unmarshal(raw, &c); err != nil || c.Candidate == "" {
		return
	}
	if err := s.pc.AddICECandidate(c); err != nil {
		log.Printf("session %s: add candidate: %v", s.id, err)
	}
}

var (
	pingPrefix    = []byte(`{"t":"p"`)
	clipPrefix    = []byte(`{"t":"clip"`)
	displayPrefix = []byte(`{"t":"display"`)
)

func (s *session) onInput(dc *webrtc.DataChannel, data []byte) {
	// Latency probe: echo back untouched so the viewer can measure the
	// round trip of its input path.
	if bytes.HasPrefix(data, pingPrefix) {
		dc.SendText(string(data))
		return
	}
	// Switching displays only changes what is watched: allowed even for
	// view-only sessions.
	if bytes.HasPrefix(data, displayPrefix) {
		var m struct{ N int }
		if json.Unmarshal(data, &m) == nil {
			s.switchDisplay(m.N)
		}
		return
	}
	// Policy is enforced here, on the controlled machine: a modified
	// viewer cannot bypass it.
	if s.inj == nil || s.policy.ViewOnly {
		return
	}
	if bytes.HasPrefix(data, clipPrefix) && !s.policy.ClipToRemote() {
		return
	}
	if err := s.inj.HandleJSON(data); err != nil {
		log.Printf("session %s: bad input event: %v", s.id, err)
	}
}

// switchDisplay moves the session to another display's pipeline.
func (s *session) switchDisplay(n int) {
	if n < 0 || n >= s.agent.displays() {
		return
	}
	old := s.current()
	if old.Display() == n {
		return
	}
	next, err := s.agent.pipelineFor(old.Codec(), n)
	if err != nil {
		log.Printf("session %s: switch to display %d: %v", s.id, n, err)
		return
	}
	if s.inj != nil {
		s.inj.Release()
	}
	s.mu.Lock()
	s.pipeline = next
	subscribed := s.subscribed && !s.closed
	s.mu.Unlock()
	if subscribed {
		next.Subscribe(s) // starts at the next keyframe
		old.Unsubscribe(s)
	}
	data, _ := json.Marshal(map[string]any{"t": "display", "n": n, "count": s.agent.displays()})
	s.sendControl(data)
	log.Printf("session %s: switched to display %d", s.id, n)
}

func (s *session) sendControl(data []byte) {
	s.mu.Lock()
	dc := s.control
	s.mu.Unlock()
	if dc != nil {
		dc.SendText(string(data))
	}
}

func (s *session) logRoute() {
	sctp := s.pc.SCTP()
	if sctp == nil {
		return
	}
	pair, err := sctp.Transport().ICETransport().GetSelectedCandidatePair()
	if err != nil || pair == nil {
		return
	}
	route := "direct"
	if pair.Local.Typ == webrtc.ICECandidateTypeRelay || pair.Remote.Typ == webrtc.ICECandidateTypeRelay {
		route = "relayed via server"
	}
	log.Printf("session %s: %s (%s:%d <-> %s:%d)", s.id, route,
		pair.Local.Address, pair.Local.Port, pair.Remote.Address, pair.Remote.Port)
}

func (s *session) subscribe() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subscribed || s.closed {
		return
	}
	s.subscribed = true
	s.pipeline.Subscribe(s)
	if s.agent.audio != nil {
		s.agent.audio.Subscribe(s)
	}
	if s.agent.sys != nil {
		s.agent.sys.SessionStarted()
	}
}

// WriteFrame implements stream.Sink.
func (s *session) WriteFrame(data []byte, d time.Duration) error {
	return s.video.WriteSample(media.Sample{Data: data, Duration: d})
}

// WriteAudio implements stream.AudioSink.
func (s *session) WriteAudio(data []byte, d time.Duration) error {
	return s.audio.WriteSample(media.Sample{Data: data, Duration: d})
}

func (s *session) close(reason string, notify bool) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	wasSubscribed := s.subscribed
	s.mu.Unlock()

	a := s.agent
	a.mu.Lock()
	delete(a.sessions, s.id)
	a.mu.Unlock()

	if wasSubscribed {
		s.current().Unsubscribe(s)
		if a.audio != nil {
			a.audio.Unsubscribe(s)
		}
		if a.sys != nil {
			a.sys.SessionEnded()
		}
	}
	if s.inj != nil {
		s.inj.Release()
	}
	s.pc.Close()
	if notify {
		a.send(protocol.Message{Type: protocol.TypeEnd, Session: s.id, Reason: reason})
	}
	log.Printf("session %s closed: %s", s.id, reason)
}
