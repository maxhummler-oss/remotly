// Package input injects mouse and keyboard events received from viewers
// into the local desktop, and watches the desktop for things viewers need
// to know about (cursor shape, clipboard, secure desktop switches).
package input

import (
	"encoding/json"
	"log"
	"sync"
	"time"
)

// Event is the wire format sent by the viewer over the data channels.
//
//	{"t":"m","x":0.5,"y":0.25}     absolute move, normalized to the stream
//	{"t":"r","dx":4,"dy":-2}       relative move in pixels (pointer lock)
//	{"t":"b","b":0,"d":true}       button (0 left, 1 middle, 2 right, 3 back, 4 forward)
//	{"t":"w","dx":0,"dy":120}      wheel; 120 = one notch, positive = up/right
//	{"t":"k","c":"KeyA","d":true}  key, KeyboardEvent.code (physical key)
//	{"t":"x"}                      release everything (viewer lost focus)
//	{"t":"sas"}                    Ctrl+Alt+Del
//	{"t":"clip","text":"…"}        clipboard text from the viewer
type Event struct {
	T    string  `json:"t"`
	X    float64 `json:"x,omitempty"`
	Y    float64 `json:"y,omitempty"`
	DX   float64 `json:"dx,omitempty"`
	DY   float64 `json:"dy,omitempty"`
	B    int     `json:"b,omitempty"`
	D    bool    `json:"d,omitempty"`
	C    string  `json:"c,omitempty"`
	Text string  `json:"text,omitempty"`
}

// Mapping tells the backend which screen area normalized coordinates cover.
type Mapping struct {
	// VirtualDesktop: coordinates span all monitors. Otherwise they span
	// the display with index Display.
	VirtualDesktop bool
	Display        int
}

// platform is implemented per OS.
type platform interface {
	moveAbs(m Mapping, x, y float64)
	moveRel(dx, dy int)
	button(b int, down bool)
	wheel(dx, dy int)
	key(code string, down bool)

	// cursor returns the CSS cursor name of the current pointer shape, or
	// "" if shape tracking is unsupported.
	cursor() string
	// desktop returns the name of the input desktop (Windows: "Default",
	// "Winlogon", …) or "" if not applicable.
	desktop() string
	getClipboard() (string, bool)
	setClipboard(text string)
	keepAwake(on bool)
	sendSAS()
}

// Options configures the system controller.
type Options struct {
	Mapping   func() Mapping
	Clipboard bool
	// Service is true when running as the Windows service worker (SYSTEM),
	// which enables secure-desktop handling and Ctrl+Alt+Del.
	Service bool
}

// Callbacks receive desktop changes.
type Callbacks struct {
	Cursor    func(css string)
	Clipboard func(text string)
	Desktop   func(name string)
}

// System is the machine-wide input controller shared by all sessions.
type System struct {
	opts Options
	p    platform

	mu       sync.Mutex
	cb       Callbacks
	sessions int
	lastClip string
	lastCur  string
	lastDesk string
}

// NewSystem initializes input injection for this OS.
func NewSystem(opts Options) (*System, error) {
	p, err := newPlatform(opts.Service)
	if err != nil {
		return nil, err
	}
	s := &System{opts: opts, p: p}
	if c, ok := p.getClipboard(); ok {
		s.lastClip = c
	}
	s.lastDesk = p.desktop()
	go s.watch()
	return s, nil
}

// SetCallbacks registers change notifications.
func (s *System) SetCallbacks(cb Callbacks) {
	s.mu.Lock()
	s.cb = cb
	s.mu.Unlock()
}

// Desktop returns the current input desktop name ("" if not applicable).
func (s *System) Desktop() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastDesk
}

// Cursor returns the last known cursor shape.
func (s *System) Cursor() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastCur
}

// SessionStarted/SessionEnded keep the machine and display awake while
// someone is connected.
func (s *System) SessionStarted() {
	s.mu.Lock()
	s.sessions++
	n := s.sessions
	s.mu.Unlock()
	if n == 1 {
		s.p.keepAwake(true)
	}
}

func (s *System) SessionEnded() {
	s.mu.Lock()
	s.sessions--
	n := s.sessions
	s.mu.Unlock()
	if n == 0 {
		s.p.keepAwake(false)
	}
}

// SetClipboard applies clipboard text from a viewer.
func (s *System) SetClipboard(text string) {
	if !s.opts.Clipboard || len(text) > 1<<20 {
		return
	}
	s.mu.Lock()
	if text == s.lastClip {
		s.mu.Unlock()
		return
	}
	s.lastClip = text // don't echo it back
	s.mu.Unlock()
	s.p.setClipboard(text)
}

// watch polls cursor shape and input desktop at display rate, and the
// clipboard once per second.
func (s *System) watch() {
	tick := time.NewTicker(16 * time.Millisecond)
	defer tick.Stop()
	n := 0
	for range tick.C {
		s.mu.Lock()
		active := s.sessions > 0
		cb := s.cb
		s.mu.Unlock()
		n++
		if !active && n%30 != 0 {
			continue // idle: only check the desktop twice a second
		}

		if d := s.p.desktop(); d != "" {
			s.mu.Lock()
			changed := d != s.lastDesk
			s.lastDesk = d
			s.mu.Unlock()
			if changed {
				log.Printf("input desktop changed: %s", d)
				if cb.Desktop != nil {
					cb.Desktop(d)
				}
			}
		}
		if !active {
			continue
		}
		if c := s.p.cursor(); c != "" {
			s.mu.Lock()
			changed := c != s.lastCur
			s.lastCur = c
			s.mu.Unlock()
			if changed && cb.Cursor != nil {
				cb.Cursor(c)
			}
		}
		if s.opts.Clipboard && n%60 == 0 {
			if text, ok := s.p.getClipboard(); ok && len(text) <= 1<<20 {
				s.mu.Lock()
				changed := text != s.lastClip
				s.lastClip = text
				s.mu.Unlock()
				if changed && text != "" && cb.Clipboard != nil {
					cb.Clipboard(text)
				}
			}
		}
	}
}

// Injector handles one viewer's input. It tracks pressed keys and buttons
// so a disconnect only releases that viewer's keys.
type Injector struct {
	sys     *System
	mu      sync.Mutex
	keys    map[string]bool
	buttons map[int]bool
}

// NewInjector creates an injector for one session.
func (s *System) NewInjector() *Injector {
	return &Injector{sys: s, keys: map[string]bool{}, buttons: map[int]bool{}}
}

// HandleJSON decodes and applies one event.
func (in *Injector) HandleJSON(data []byte) error {
	var ev Event
	if err := json.Unmarshal(data, &ev); err != nil {
		return err
	}
	in.Handle(ev)
	return nil
}

// Handle applies one event.
func (in *Injector) Handle(ev Event) {
	p := in.sys.p
	in.mu.Lock()
	defer in.mu.Unlock()
	switch ev.T {
	case "m":
		p.moveAbs(in.sys.opts.Mapping(), clamp01(ev.X), clamp01(ev.Y))
	case "r":
		p.moveRel(clampInt(ev.DX), clampInt(ev.DY))
	case "b":
		if ev.B < 0 || ev.B > 4 || in.buttons[ev.B] == ev.D {
			return
		}
		in.buttons[ev.B] = ev.D
		p.button(ev.B, ev.D)
	case "w":
		p.wheel(clampInt(ev.DX), clampInt(ev.DY))
	case "k":
		if !knownKey(ev.C) || (!ev.D && !in.keys[ev.C]) {
			return
		}
		if ev.D {
			in.keys[ev.C] = true
		} else {
			delete(in.keys, ev.C)
		}
		p.key(ev.C, ev.D)
	case "x":
		in.releaseLocked()
	case "sas":
		in.releaseLocked()
		p.sendSAS()
	case "clip":
		in.sys.SetClipboard(ev.Text)
	}
}

// Release lifts all keys and buttons still held by this viewer.
func (in *Injector) Release() {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.releaseLocked()
}

func (in *Injector) releaseLocked() {
	for code := range in.keys {
		in.sys.p.key(code, false)
	}
	in.keys = map[string]bool{}
	for b, down := range in.buttons {
		if down {
			in.sys.p.button(b, false)
		}
	}
	in.buttons = map[int]bool{}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func clampInt(v float64) int {
	if v > 10000 {
		return 10000
	}
	if v < -10000 {
		return -10000
	}
	return int(v)
}
