package stream

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// AudioSink receives Opus packets.
type AudioSink interface {
	WriteAudio(packet []byte, duration time.Duration) error
}

// audioSource describes how system audio reaches ffmpeg.
type audioSource struct {
	name  string
	input []string // ffmpeg input args; "pipe:0" when fed by loopback
	// loopback, if set, captures audio itself and writes raw PCM to w.
	loopback func(ctx context.Context, w io.Writer) error
}

// ResolveAudio picks the system audio source. ok=false means audio is off.
func ResolveAudio(opts Options) (audioSource, bool) {
	if opts.Audio == "off" {
		return audioSource{}, false
	}
	if len(opts.AudioArgs) > 0 {
		return audioSource{name: "custom", input: opts.AudioArgs}, true
	}
	if !hasComponent(opts.FFmpeg, "-encoders", "libopus") {
		log.Printf("audio disabled: ffmpeg has no libopus encoder")
		return audioSource{}, false
	}
	switch runtime.GOOS {
	case "windows":
		// Native WASAPI loopback of the default playback device.
		format, err := probeLoopback()
		if err != nil {
			log.Printf("audio disabled: %v (install a playback device or a virtual audio cable)", err)
			return audioSource{}, false
		}
		return audioSource{
			name:     "wasapi-loopback",
			input:    append(format, "-i", "pipe:0"),
			loopback: captureLoopback,
		}, true
	case "darwin":
		// macOS has no loopback; a virtual device such as BlackHole is needed.
		if dev := findAVFoundationAudio(opts.FFmpeg, "BlackHole"); dev != "" {
			return audioSource{name: "avfoundation:" + dev, input: []string{"-f", "avfoundation", "-i", ":" + dev}}, true
		}
		log.Printf("audio disabled: install BlackHole (brew install blackhole-2ch) and use it as output device")
		return audioSource{}, false
	default:
		return audioSource{name: "pulse-monitor", input: []string{"-f", "pulse", "-i", "@DEFAULT_MONITOR@"}}, true
	}
}

var avAudioRE = regexp.MustCompile(`\[(\d+)\] (.+)$`)

func findAVFoundationAudio(ffmpeg, want string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _ := ffmpegOutput(ctx, ffmpeg, "-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", "")
	audio := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "audio devices") {
			audio = true
			continue
		}
		if m := avAudioRE.FindStringSubmatch(strings.TrimSpace(line)); audio && m != nil && strings.Contains(m[2], want) {
			return strings.TrimSpace(m[2])
		}
	}
	return ""
}

// AudioPipeline captures system audio and encodes it to Opus with 10 ms
// frames (lowest practical latency), shared by all viewers.
type AudioPipeline struct {
	opts Options
	src  audioSource

	mu      sync.Mutex
	subs    map[AudioSink]struct{}
	running bool
	stop    chan struct{}
	stopped chan struct{}
}

// NewAudioPipeline returns nil if audio is unavailable.
func NewAudioPipeline(opts Options) *AudioPipeline {
	src, ok := ResolveAudio(opts)
	if !ok {
		return nil
	}
	log.Printf("audio source: %s", src.name)
	return &AudioPipeline{opts: opts, src: src, subs: map[AudioSink]struct{}{}}
}

// Subscribe adds a sink.
func (a *AudioPipeline) Subscribe(s AudioSink) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.subs[s] = struct{}{}
	if !a.running {
		a.running = true
		a.stop = make(chan struct{})
		a.stopped = make(chan struct{})
		go a.loop(a.stop, a.stopped)
	}
}

// Unsubscribe removes a sink and stops capture when nobody listens.
func (a *AudioPipeline) Unsubscribe(s AudioSink) {
	a.mu.Lock()
	delete(a.subs, s)
	var stopped chan struct{}
	if len(a.subs) == 0 && a.running {
		a.running = false
		close(a.stop)
		stopped = a.stopped
	}
	a.mu.Unlock()
	if stopped != nil {
		<-stopped
	}
}

func (a *AudioPipeline) loop(stop, stopped chan struct{}) {
	defer close(stopped)
	backoff := time.Second
	for {
		started := time.Now()
		err := a.runOnce(stop)
		select {
		case <-stop:
			return
		default:
		}
		if time.Since(started) > 30*time.Second {
			backoff = time.Second
		}
		log.Printf("audio stopped: %v (restarting in %v)", err, backoff)
		select {
		case <-stop:
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (a *AudioPipeline) runOnce(stop chan struct{}) error {
	args := []string{"-hide_banner", "-loglevel", "warning", "-fflags", "nobuffer"}
	args = append(args, a.src.input...)
	args = append(args, "-vn", "-ac", "2", "-ar", "48000",
		"-c:a", "libopus", "-b:a", "128k", "-application", "lowdelay", "-frame_duration", "10",
		"-flush_packets", "1", "-page_duration", "10000", "-f", "ogg", "pipe:1")
	proc, err := ExecLauncher(a.opts.FFmpeg, args, a.src.loopback != nil)
	if err != nil {
		return err
	}
	go logStderr(proc.Stderr(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-stop:
		case <-ctx.Done():
		}
		proc.Kill()
	}()
	if a.src.loopback != nil {
		go func() {
			if err := a.src.loopback(ctx, proc.Stdin()); err != nil && ctx.Err() == nil {
				log.Printf("audio loopback: %v", err)
			}
			proc.Kill()
		}()
	}

	err = a.pump(proc.Stdout())
	cancel()
	proc.Kill()
	proc.Wait()
	return err
}

func (a *AudioPipeline) pump(r io.Reader) error {
	or := newOggReader(r)
	for {
		pkt, err := or.next()
		if err != nil {
			return err
		}
		if len(pkt) >= 8 && (string(pkt[:8]) == "OpusHead" || string(pkt[:8]) == "OpusTags") {
			continue
		}
		d := opusDuration(pkt)
		if d == 0 {
			continue
		}
		a.mu.Lock()
		for s := range a.subs {
			s.WriteAudio(pkt, d)
		}
		a.mu.Unlock()
	}
}

// opusDuration decodes the packet duration from the TOC byte (RFC 6716 3.1).
func opusDuration(p []byte) time.Duration {
	if len(p) == 0 {
		return 0
	}
	cfg := p[0] >> 3
	var frame time.Duration
	switch {
	case cfg < 12: // SILK: 10, 20, 40, 60 ms
		frame = []time.Duration{10, 20, 40, 60}[cfg%4] * time.Millisecond
	case cfg < 16: // Hybrid: 10, 20 ms
		frame = []time.Duration{10, 20}[cfg%2] * time.Millisecond
	default: // CELT: 2.5, 5, 10, 20 ms
		frame = []time.Duration{2500, 5000, 10000, 20000}[cfg%4] * time.Microsecond
	}
	n := 1
	switch p[0] & 3 {
	case 1, 2:
		n = 2
	case 3:
		if len(p) < 2 {
			return 0
		}
		n = int(p[1] & 0x3f)
	}
	return frame * time.Duration(n)
}

// oggReader splits an Ogg stream into packets.
type oggReader struct {
	r     *bufio.Reader
	queue [][]byte
	carry []byte
}

func newOggReader(r io.Reader) *oggReader {
	return &oggReader{r: bufio.NewReaderSize(r, 64<<10)}
}

func (o *oggReader) next() ([]byte, error) {
	for len(o.queue) == 0 {
		if err := o.readPage(); err != nil {
			return nil, err
		}
	}
	p := o.queue[0]
	o.queue = o.queue[1:]
	return p, nil
}

func (o *oggReader) readPage() error {
	var hdr [27]byte
	if _, err := io.ReadFull(o.r, hdr[:]); err != nil {
		return err
	}
	if string(hdr[:4]) != "OggS" {
		return errors.New("ogg: lost sync")
	}
	_ = binary.LittleEndian.Uint64(hdr[6:14]) // granule position (unused)
	nseg := int(hdr[26])
	lacing := make([]byte, nseg)
	if _, err := io.ReadFull(o.r, lacing); err != nil {
		return err
	}
	total := 0
	for _, l := range lacing {
		total += int(l)
	}
	body := make([]byte, total)
	if _, err := io.ReadFull(o.r, body); err != nil {
		return err
	}
	if hdr[5]&1 == 0 {
		o.carry = nil // not a continuation: drop any partial packet
	}
	pos := 0
	for _, l := range lacing {
		o.carry = append(o.carry, body[pos:pos+int(l)]...)
		pos += int(l)
		if l < 255 {
			o.queue = append(o.queue, o.carry)
			o.carry = nil
		}
	}
	if len(o.carry) > 1<<20 {
		return fmt.Errorf("ogg: oversized packet")
	}
	return nil
}
