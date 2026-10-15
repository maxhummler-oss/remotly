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
	sink         Sink
	needKeyframe bool
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
}

// NewPipeline creates an idle pipeline.
func NewPipeline(opts Options, encoder string) *Pipeline {
	if opts.Launch == nil {
		opts.Launch = ExecLauncher
	}
	return &Pipeline{opts: opts, encoder: encoder, variants: Variants(opts, encoder), subs: map[Sink]*subscriber{}}
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

// Subscribe adds a sink. It receives frames starting at the next keyframe.
func (p *Pipeline) Subscribe(s Sink) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subs[s] = &subscriber{sink: s, needKeyframe: true}
	if !p.running {
		p.running = true
		p.stop = make(chan struct{})
		p.stopped = make(chan struct{})
		go p.loop(p.stop, p.stopped)
	} else if p.proc != nil {
		// A new viewer needs a keyframe now, not at the end of the GOP:
		// restarting ffmpeg yields one within a few frames.
		p.restart = true
		p.lastKF = time.Now()
		p.proc.Kill()
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
// so it is restarted, at most every 3 seconds.
func (p *Pipeline) RequestKeyframe() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.proc == nil || time.Since(p.lastKF) < 3*time.Second {
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
	p.mu.Unlock()

	args := BuildArgs(p.encoder, v, p.opts)
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
			if sub.needKeyframe {
				if !frame.Keyframe {
					continue
				}
				sub.needKeyframe = false
			}
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
