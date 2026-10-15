package stream

import (
	"bufio"
	"errors"
	"io"
	"log"
	"strings"
	"sync"
	"time"
)

// Sink receives encoded frames. Implementations must not block for long:
// a slow sink delays every viewer.
type Sink interface {
	WriteFrame(data []byte, duration time.Duration) error
}

type subscriber struct {
	sink Sink
}

// Pipeline runs one ffmpeg capture/encode process shared by all viewers of
// this machine. It starts with the first subscriber and stops with the
// last. If a capture variant keeps failing it falls back to the next one.
type Pipeline struct {
	opts     Options
	encoder  string
	variants []Variant

	mu      sync.Mutex
	current int  // index into variants
	proven  bool // current variant has delivered frames before
	subs    map[Sink]*subscriber
	running bool
	stop    chan struct{}
	stopped chan struct{}
	proc    Process
	restart bool
	lastKF  time.Time // last keyframe-forcing restart

	// Adaptive bitrate (see ReportLoss).
	maxKbps    int // configured bitrate, possibly capped by the server
	curKbps    int // bitrate ffmpeg currently runs with
	lossy      int // consecutive loss reports above threshold
	lastChange time.Time
	lastLoss   time.Time
}

const (
	minKbps       = 3000
	lossThreshold = 0.04 // 4 % packet loss
)

// NewPipeline creates an idle pipeline.
func NewPipeline(opts Options, encoder string) *Pipeline {
	if opts.Launch == nil {
		opts.Launch = ExecLauncher
	}
	return &Pipeline{opts: opts, encoder: encoder, variants: Variants(opts, encoder), subs: map[Sink]*subscriber{},
		maxKbps: opts.BitrateKbps, curKbps: opts.BitrateKbps}
}

// Display returns the display this pipeline captures.
func (p *Pipeline) Display() int { return p.opts.Display }

// Bitrate returns the current target bitrate in kbit/s.
func (p *Pipeline) Bitrate() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.curKbps
}

// SetCap limits the bitrate (server policy for shared uplinks); 0 = none.
func (p *Pipeline) SetCap(capKbps int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	max := p.opts.BitrateKbps
	if capKbps > 0 && capKbps < max {
		max = capKbps
	}
	if max == p.maxKbps {
		return
	}
	p.maxKbps = max
	if p.curKbps > max {
		p.setBitrateLocked(max, "server cap")
	}
}

// ReportLoss feeds the fraction of packets a viewer lost (0..1, from RTCP
// receiver reports, about once per second). Sustained loss means the
// network is saturated – typically many sessions sharing one uplink – so
// the bitrate steps down; after a quiet period it climbs back. ffmpeg
// cannot change bitrate on the fly, so a change restarts the encoder
// (which also yields a fresh keyframe).
func (p *Pipeline) ReportLoss(fraction float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if fraction > lossThreshold {
		p.lossy++
		p.lastLoss = now
		if p.lossy >= 2 && now.Sub(p.lastChange) > 4*time.Second && p.curKbps > minKbps {
			p.setBitrateLocked(max(minKbps, p.curKbps*7/10), "packet loss")
		}
		return
	}
	p.lossy = 0
	if p.curKbps < p.maxKbps && now.Sub(p.lastLoss) > 30*time.Second && now.Sub(p.lastChange) > 30*time.Second {
		p.setBitrateLocked(min(p.maxKbps, p.curKbps*3/2), "network recovered")
	}
}

func (p *Pipeline) setBitrateLocked(kbps int, why string) {
	if kbps == p.curKbps {
		return
	}
	log.Printf("bitrate %d -> %d kbit/s (%s)", p.curKbps, kbps, why)
	p.curKbps = kbps
	p.lastChange = time.Now()
	p.lossy = 0
	if p.proc != nil {
		p.restart = true
		p.proc.Kill()
	}
}

// FPS returns the capture frame rate.
func (p *Pipeline) FPS() int { return p.opts.FPS }

// Codec returns the codec this pipeline produces.
func (p *Pipeline) Codec() Codec { return CodecOf(p.encoder) }

// Encoder returns the encoder in use.
func (p *Pipeline) Encoder() string { return p.encoder }

// Capture returns the current capture description.
func (p *Pipeline) Capture() CaptureInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.variants[p.current].Capture
}

// Variant returns the name of the current capture variant.
func (p *Pipeline) Variant() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.variants[p.current].Name
}

// Subscribe adds a sink. It receives every frame from now on. Frames are
// deliberately not held back until the next keyframe: browsers discard the
// few undecodable frames and recover cleanly, whereas a stream that starts
// mid-way at a keyframe confuses Chrome's VP8 frame-reference tracking
// until it sends its own PLI (~3 s). A keyframe is forced right after
// joining anyway.
func (p *Pipeline) Subscribe(s Sink) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subs[s] = &subscriber{sink: s}
	if !p.running {
		p.running = true
		p.stop = make(chan struct{})
		p.stopped = make(chan struct{})
		go p.loop(p.stop, p.stopped)
	} else {
		// A new viewer needs a keyframe now, not at the end of the GOP;
		// restarting ffmpeg yields one within a few frames. The short delay
		// lets the new connection settle on the browser side first.
		time.AfterFunc(150*time.Millisecond, p.forceKeyframe)
	}
}

// Unsubscribe removes a sink and stops ffmpeg when nobody is watching.
func (p *Pipeline) Unsubscribe(s Sink) {
	p.mu.Lock()
	delete(p.subs, s)
	var stopped chan struct{}
	if len(p.subs) == 0 && p.running {
		p.running = false
		close(p.stop)
		stopped = p.stopped
	}
	p.mu.Unlock()
	if stopped != nil {
		<-stopped
	}
}

// Restart restarts the capture process (e.g. after a desktop switch).
func (p *Pipeline) Restart() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.proc != nil {
		p.restart = true
		p.proc.Kill()
	}
}

// RequestKeyframe is called when a viewer reports unrecoverable packet
// loss (RTCP PLI/FIR). ffmpeg cannot be asked for an IDR frame on the fly,
// so it is restarted, at most once per second.
func (p *Pipeline) RequestKeyframe() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.proc == nil || time.Since(p.lastKF) < time.Second {
		return
	}
	p.lastKF = time.Now()
	p.restart = true
	p.proc.Kill()
}

func (p *Pipeline) forceKeyframe() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running || p.proc == nil {
		return
	}
	p.lastKF = time.Now()
	p.restart = true
	p.proc.Kill()
}

// loop keeps ffmpeg running until stop is closed.
func (p *Pipeline) loop(stop, stopped chan struct{}) {
	defer close(stopped)
	backoff := 500 * time.Millisecond
	failures := 0
	for {
		started := time.Now()
		frames, err := p.runOnce(stop)
		select {
		case <-stop:
			return
		default:
		}

		p.mu.Lock()
		intentional := p.restart
		p.restart = false
		if frames > 0 {
			p.proven = true
			failures = 0
			backoff = 500 * time.Millisecond
		} else if !intentional {
			failures++
			// A variant that never produced a frame gets two chances,
			// then the next (slower but more compatible) one is tried.
			if !p.proven && failures >= 2 && len(p.variants) > 1 {
				next := (p.current + 1) % len(p.variants)
				log.Printf("capture variant %q failed, falling back to %q", p.variants[p.current].Name, p.variants[next].Name)
				p.current = next
				failures = 0
			}
		}
		p.mu.Unlock()

		if intentional {
			continue
		}
		log.Printf("capture stopped after %v: %v (restarting in %v)", time.Since(started).Round(time.Millisecond), err, backoff)
		select {
		case <-stop:
			return
		case <-time.After(backoff):
		}
		if frames == 0 {
			backoff = min(backoff*2, 10*time.Second)
		}
	}
}

func (p *Pipeline) runOnce(stop chan struct{}) (int, error) {
	p.mu.Lock()
	v := p.variants[p.current]
	opts := p.opts
	opts.BitrateKbps = p.curKbps
	p.mu.Unlock()

	args := BuildArgs(p.encoder, v, opts)
	proc, err := p.opts.Launch(p.opts.FFmpeg, args, false)
	if err != nil {
		return 0, err
	}
	p.mu.Lock()
	p.proc = proc
	p.mu.Unlock()
	log.Printf("capture started (%s, %s): %s", v.Name, p.encoder, strings.Join(args, " "))

	stderrTail := newTail(8)
	go logStderr(proc.Stderr(), stderrTail)

	done := make(chan struct{})
	go func() {
		select {
		case <-stop:
			proc.Kill()
		case <-done:
		}
	}()

	frames, err := p.pump(proc.Stdout())
	close(done)
	proc.Kill()
	waitErr := proc.Wait()

	p.mu.Lock()
	p.proc = nil
	p.mu.Unlock()

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		err = waitErr
	}
	if frames == 0 && err != nil {
		if tail := stderrTail.String(); tail != "" {
			err = errors.New(err.Error() + ": " + tail)
		}
	}
	return frames, err
}

// pump reads frames and fans them out. Timestamps come from the wall clock
// so the browser renders each frame as soon as it arrives.
func (p *Pipeline) pump(r io.Reader) (int, error) {
	var fr frameReader
	if CodecOf(p.encoder) == VP8 {
		fr = newIVFReader(r)
	} else {
		fr = newFLVReader(r)
	}
	if err := fr.readHeader(); err != nil {
		return 0, err
	}
	frames := 0
	last := time.Now()
	for {
		frame, err := fr.Next()
		if err != nil {
			return frames, err
		}
		frames++
		now := time.Now()
		dur := now.Sub(last)
		last = now

		p.mu.Lock()
		for _, sub := range p.subs {
			if err := sub.sink.WriteFrame(frame.Data, dur); err != nil && !errors.Is(err, io.ErrClosedPipe) {
				log.Printf("write frame: %v", err)
			}
		}
		p.mu.Unlock()
	}
}

// tail keeps the last n lines of ffmpeg's stderr for error reports.
type tail struct {
	mu    sync.Mutex
	lines []string
	n     int
}

func newTail(n int) *tail { return &tail{n: n} }

func (t *tail) add(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, s)
	if len(t.lines) > t.n {
		t.lines = t.lines[1:]
	}
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, " | ")
}

func logStderr(r io.Reader, t *tail) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		log.Printf("ffmpeg: %s", line)
		if t != nil {
			t.add(line)
		}
	}
}
