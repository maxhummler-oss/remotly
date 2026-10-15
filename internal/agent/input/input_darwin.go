package input

import (
	"fmt"
	"math"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ebitengine/purego"
)

// macOS backend: CoreGraphics event injection loaded at runtime with purego,
// so the agent cross-compiles without cgo. Requires the Accessibility
// permission (System Settings > Privacy & Security > Accessibility).

type cgPoint struct{ X, Y float64 }

type cgRect struct {
	Origin cgPoint
	Size   cgPoint
}

const (
	cgEventLeftMouseDown     = 1
	cgEventLeftMouseUp       = 2
	cgEventRightMouseDown    = 3
	cgEventRightMouseUp      = 4
	cgEventMouseMoved        = 5
	cgEventLeftMouseDragged  = 6
	cgEventRightMouseDragged = 7
	cgEventOtherMouseDown    = 25
	cgEventOtherMouseUp      = 26
	cgEventOtherMouseDragged = 27

	cgHIDEventTap             = 0
	cgEventSourceStateHID     = 1
	cgScrollEventUnitPixel    = 0
	cgMouseEventClickState    = 1
	cgMouseEventDeltaX        = 4
	cgMouseEventDeltaY        = 5
	cgMouseEventButtonNumber  = 3
	cgEventFlagMaskAlphaShift = 0x00010000
	cgEventFlagMaskShift      = 0x00020000
	cgEventFlagMaskControl    = 0x00040000
	cgEventFlagMaskAlternate  = 0x00080000
	cgEventFlagMaskCommand    = 0x00100000
	cgEventFlagMaskNumericPad = 0x00200000
)

var (
	loadOnce sync.Once
	loadErr  error

	cgEventSourceCreate            func(state int32) uintptr
	cgEventCreateMouseEvent        func(src uintptr, typ uint32, pos cgPoint, button uint32) uintptr
	cgEventCreateKeyboardEvent     func(src uintptr, key uint16, down bool) uintptr
	cgEventCreateScrollWheelEvent2 func(src uintptr, units uint32, count uint32, w1, w2, w3 int32) uintptr
	cgEventSetFlags                func(ev uintptr, flags uint64)
	cgEventSetIntegerValueField    func(ev uintptr, field uint32, value int64)
	cgEventPost                    func(tap uint32, ev uintptr)
	cgMainDisplayID                func() uint32
	cgGetActiveDisplayList         func(max uint32, displays *uint32, count *uint32) int32
	cgDisplayBounds                func(display uint32) cgRect
	cgEventCreate                  func(src uintptr) uintptr
	cgEventGetLocation             func(ev uintptr) cgPoint
	cfRelease                      func(ref uintptr)
)

func loadCG() error {
	loadOnce.Do(func() {
		cg, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = fmt.Errorf("load CoreGraphics: %w", err)
			return
		}
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = fmt.Errorf("load CoreFoundation: %w", err)
			return
		}
		purego.RegisterLibFunc(&cgEventSourceCreate, cg, "CGEventSourceCreate")
		purego.RegisterLibFunc(&cgEventCreateMouseEvent, cg, "CGEventCreateMouseEvent")
		purego.RegisterLibFunc(&cgEventCreateKeyboardEvent, cg, "CGEventCreateKeyboardEvent")
		purego.RegisterLibFunc(&cgEventCreateScrollWheelEvent2, cg, "CGEventCreateScrollWheelEvent2")
		purego.RegisterLibFunc(&cgEventSetFlags, cg, "CGEventSetFlags")
		purego.RegisterLibFunc(&cgEventSetIntegerValueField, cg, "CGEventSetIntegerValueField")
		purego.RegisterLibFunc(&cgEventPost, cg, "CGEventPost")
		purego.RegisterLibFunc(&cgMainDisplayID, cg, "CGMainDisplayID")
		purego.RegisterLibFunc(&cgGetActiveDisplayList, cg, "CGGetActiveDisplayList")
		purego.RegisterLibFunc(&cgDisplayBounds, cg, "CGDisplayBounds")
		purego.RegisterLibFunc(&cgEventCreate, cg, "CGEventCreate")
		purego.RegisterLibFunc(&cgEventGetLocation, cg, "CGEventGetLocation")
		purego.RegisterLibFunc(&cfRelease, cf, "CFRelease")
	})
	return loadErr
}

type macBackend struct {
	mu  sync.Mutex
	src uintptr

	display  int
	pos      cgPoint
	buttons  [5]bool
	flags    uint64
	lastDown time.Time
	lastPos  cgPoint
	lastBtn  int
	clicks   int64

	caffeinate *exec.Cmd
}

func newPlatform(bool) (platform, error) {
	if err := loadCG(); err != nil {
		return nil, err
	}
	b := &macBackend{src: cgEventSourceCreate(cgEventSourceStateHID)}
	if ev := cgEventCreate(0); ev != 0 {
		b.pos = cgEventGetLocation(ev)
		cfRelease(ev)
	}
	return b, nil
}

// bounds returns the captured display's rectangle in global coordinates
// (points). Looked up on every move so display changes are picked up.
func (m *macBackend) bounds(display int) cgRect {
	var ids [16]uint32
	var n uint32
	id := cgMainDisplayID()
	if cgGetActiveDisplayList(uint32(len(ids)), &ids[0], &n) == 0 && display < int(n) {
		id = ids[display]
	}
	return cgDisplayBounds(id)
}

func (m *macBackend) post(ev uintptr) {
	if ev == 0 {
		return
	}
	cgEventSetFlags(ev, m.flags)
	cgEventPost(cgHIDEventTap, ev)
	cfRelease(ev)
}

// moveType returns the event type for pointer motion given held buttons:
// macOS apps expect "dragged" events while a button is down.
func (m *macBackend) moveType() (uint32, uint32) {
	switch {
	case m.buttons[0]:
		return cgEventLeftMouseDragged, 0
	case m.buttons[2]:
		return cgEventRightMouseDragged, 1
	case m.buttons[1]:
		return cgEventOtherMouseDragged, 2
	}
	return cgEventMouseMoved, 0
}

func (m *macBackend) moveTo(p cgPoint, dx, dy int64) {
	m.pos = p
	typ, btn := m.moveType()
	ev := cgEventCreateMouseEvent(m.src, typ, p, btn)
	if ev != 0 && (dx != 0 || dy != 0) {
		cgEventSetIntegerValueField(ev, cgMouseEventDeltaX, dx)
		cgEventSetIntegerValueField(ev, cgMouseEventDeltaY, dy)
	}
	m.post(ev)
}

func (m *macBackend) moveAbs(mp Mapping, x, y float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.display = mp.Display
	r := m.bounds(mp.Display)
	p := cgPoint{
		X: r.Origin.X + x*(r.Size.X-1),
		Y: r.Origin.Y + y*(r.Size.Y-1),
	}
	m.moveTo(p, int64(p.X-m.pos.X), int64(p.Y-m.pos.Y))
}

func (m *macBackend) moveRel(dx, dy int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.bounds(m.display)
	p := cgPoint{
		X: math.Max(r.Origin.X, math.Min(r.Origin.X+r.Size.X-1, m.pos.X+float64(dx))),
		Y: math.Max(r.Origin.Y, math.Min(r.Origin.Y+r.Size.Y-1, m.pos.Y+float64(dy))),
	}
	m.moveTo(p, int64(dx), int64(dy))
}

func (m *macBackend) button(b int, down bool) {
	var typ, cgBtn uint32
	switch b {
	case 0:
		typ, cgBtn = pick(down, cgEventLeftMouseDown, cgEventLeftMouseUp), 0
	case 2:
		typ, cgBtn = pick(down, cgEventRightMouseDown, cgEventRightMouseUp), 1
	case 1:
		typ, cgBtn = pick(down, cgEventOtherMouseDown, cgEventOtherMouseUp), 2
	case 3, 4:
		typ, cgBtn = pick(down, cgEventOtherMouseDown, cgEventOtherMouseUp), uint32(b)
	default:
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buttons[b] = down

	// Click counting turns two quick clicks into a double click.
	if down {
		now := time.Now()
		near := math.Abs(m.pos.X-m.lastPos.X) < 5 && math.Abs(m.pos.Y-m.lastPos.Y) < 5
		if b == m.lastBtn && near && now.Sub(m.lastDown) < 500*time.Millisecond {
			m.clicks++
		} else {
			m.clicks = 1
		}
		m.lastDown, m.lastPos, m.lastBtn = now, m.pos, b
	}

	ev := cgEventCreateMouseEvent(m.src, typ, m.pos, cgBtn)
	if ev != 0 {
		cgEventSetIntegerValueField(ev, cgMouseEventClickState, max(m.clicks, 1))
		if cgBtn > 1 {
			cgEventSetIntegerValueField(ev, cgMouseEventButtonNumber, int64(cgBtn))
		}
	}
	m.post(ev)
}

func (m *macBackend) wheel(dx, dy int) {
	// 120 units = one notch ≈ 40 pixels of scrolling.
	px := func(v int) int32 { return int32(v * 40 / 120) }
	if dx == 0 && dy == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ev := cgEventCreateScrollWheelEvent2(m.src, cgScrollEventUnitPixel, 2, px(dy), -px(dx), 0)
	m.post(ev)
}

var macModifiers = map[string]uint64{
	"ShiftLeft":    cgEventFlagMaskShift,
	"ShiftRight":   cgEventFlagMaskShift,
	"ControlLeft":  cgEventFlagMaskControl,
	"ControlRight": cgEventFlagMaskControl,
	"AltLeft":      cgEventFlagMaskAlternate,
	"AltRight":     cgEventFlagMaskAlternate,
	"MetaLeft":     cgEventFlagMaskCommand,
	"MetaRight":    cgEventFlagMaskCommand,
}

func (m *macBackend) key(code string, down bool) {
	def := keymap[code]
	if def.mac == macNone {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if mask, ok := macModifiers[code]; ok {
		if down {
			m.flags |= mask
		} else {
			m.flags &^= mask
		}
	}
	if code == "CapsLock" && down {
		m.flags ^= cgEventFlagMaskAlphaShift
	}
	ev := cgEventCreateKeyboardEvent(m.src, def.mac, down)
	if ev == 0 {
		return
	}
	flags := m.flags
	if strings.HasPrefix(code, "Numpad") || strings.HasPrefix(code, "Arrow") {
		flags |= cgEventFlagMaskNumericPad
	}
	cgEventSetFlags(ev, flags)
	cgEventPost(cgHIDEventTap, ev)
	cfRelease(ev)
}

// macOS cannot report the cursor shape without AppKit, so the cursor is
// drawn into the video instead (localCursor "auto" = off on macOS).
func (m *macBackend) cursor() string  { return "" }
func (m *macBackend) desktop() string { return "" }

func (m *macBackend) getClipboard() (string, bool) {
	out, err := exec.Command("pbpaste", "-Prefer", "txt").Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

func (m *macBackend) setClipboard(text string) {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	cmd.Run()
}

// keepAwake runs caffeinate while viewers are connected.
func (m *macBackend) keepAwake(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if on && m.caffeinate == nil {
		cmd := exec.Command("caffeinate", "-d", "-i", "-u")
		if cmd.Start() == nil {
			m.caffeinate = cmd
			go cmd.Wait()
		}
	} else if !on && m.caffeinate != nil {
		m.caffeinate.Process.Kill()
		m.caffeinate = nil
	}
}

func (m *macBackend) sendSAS() {}

func (m *macBackend) displays() int {
	var ids [16]uint32
	var n uint32
	if cgGetActiveDisplayList(uint32(len(ids)), &ids[0], &n) != 0 || n == 0 {
		return 1
	}
	return int(n)
}

func pick(cond bool, a, b uint32) uint32 {
	if cond {
		return a
	}
	return b
}
