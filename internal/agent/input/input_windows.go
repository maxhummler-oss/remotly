package input

import (
	"fmt"
	"log"
	"math"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                        = windows.NewLazySystemDLL("user32.dll")
	kernel32                      = windows.NewLazySystemDLL("kernel32.dll")
	procSendInput                 = user32.NewProc("SendInput")
	procSetProcessDpiAwarenessCtx = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware        = user32.NewProc("SetProcessDPIAware")
	procOpenInputDesktop          = user32.NewProc("OpenInputDesktop")
	procSetThreadDesktop          = user32.NewProc("SetThreadDesktop")
	procCloseDesktop              = user32.NewProc("CloseDesktop")
	procGetUserObjectInformation  = user32.NewProc("GetUserObjectInformationW")
	procGetCursorInfo             = user32.NewProc("GetCursorInfo")
	procLoadCursor                = user32.NewProc("LoadCursorW")
	procGetSystemMetrics          = user32.NewProc("GetSystemMetrics")
	procEnumDisplayMonitors       = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfo            = user32.NewProc("GetMonitorInfoW")
	procOpenClipboard             = user32.NewProc("OpenClipboard")
	procCloseClipboard            = user32.NewProc("CloseClipboard")
	procEmptyClipboard            = user32.NewProc("EmptyClipboard")
	procGetClipboardData          = user32.NewProc("GetClipboardData")
	procSetClipboardData          = user32.NewProc("SetClipboardData")
	procIsClipboardFormatAvail    = user32.NewProc("IsClipboardFormatAvailable")
	procGetClipboardSequenceNum   = user32.NewProc("GetClipboardSequenceNumber")
	procGlobalAlloc               = kernel32.NewProc("GlobalAlloc")
	procGlobalFree                = kernel32.NewProc("GlobalFree")
	procGlobalLock                = kernel32.NewProc("GlobalLock")
	procGlobalUnlock              = kernel32.NewProc("GlobalUnlock")
	procSetThreadExecutionState   = kernel32.NewProc("SetThreadExecutionState")
)

const (
	inputMouse    = 0
	inputKeyboard = 1

	mouseMove        = 0x0001
	mouseLeftDown    = 0x0002
	mouseLeftUp      = 0x0004
	mouseRightDown   = 0x0008
	mouseRightUp     = 0x0010
	mouseMiddleDown  = 0x0020
	mouseMiddleUp    = 0x0040
	mouseXDown       = 0x0080
	mouseXUp         = 0x0100
	mouseWheel       = 0x0800
	mouseHWheel      = 0x1000
	mouseNoCoalesce  = 0x2000
	mouseVirtualDesk = 0x4000
	mouseAbsolute    = 0x8000

	keyExtended = 0x0001
	keyUp       = 0x0002
	keyScancode = 0x0008

	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79

	cfUnicodeText = 13
	gmemMoveable  = 0x0002
	uoiName       = 2
	genericAll    = 0x10000000
	cursorShowing = 0x00000001

	esContinuous      = 0x80000000
	esSystemRequired  = 0x00000001
	esDisplayRequired = 0x00000002
)

type mouseInput struct {
	dx, dy    int32
	mouseData uint32
	flags     uint32
	time      uint32
	extra     uintptr
}

type keybdInput struct {
	vk, scan uint16
	flags    uint32
	time     uint32
	extra    uintptr
}

// winInput mirrors the Win32 INPUT struct. The union is stored as
// mouseInput (its largest member) and reinterpreted for keyboard input.
type winInput struct {
	typ uint32
	mi  mouseInput
}

type rect struct{ Left, Top, Right, Bottom int32 }

type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

type cursorInfo struct {
	Size   uint32
	Flags  uint32
	Cursor uintptr
	X, Y   int32
}

// CSS names for the standard Windows cursors (IDC_* resource IDs).
var standardCursors = map[uintptr]string{
	32512: "default",     // IDC_ARROW
	32513: "text",        // IDC_IBEAM
	32514: "wait",        // IDC_WAIT
	32515: "crosshair",   // IDC_CROSS
	32516: "default",     // IDC_UPARROW
	32642: "nwse-resize", // IDC_SIZENWSE
	32643: "nesw-resize", // IDC_SIZENESW
	32644: "ew-resize",   // IDC_SIZEWE
	32645: "ns-resize",   // IDC_SIZENS
	32646: "move",        // IDC_SIZEALL
	32648: "not-allowed", // IDC_NO
	32649: "pointer",     // IDC_HAND
	32650: "progress",    // IDC_APPSTARTING
	32651: "help",        // IDC_HELP
}

// winPlatform runs every Win32 call on one dedicated, locked OS thread.
// That thread follows the input desktop (Default, Winlogon for the lock
// and logon screens, the secure UAC desktop) via SetThreadDesktop, which is
// only possible for a thread that owns no windows – and only works when
// the agent runs as SYSTEM (service worker).
type winPlatform struct {
	jobs     chan func()
	service  bool
	deskName string
	desk     uintptr
	cursors  map[uintptr]string
	clipSeq  uintptr
	clipText string
}

func newPlatform(service bool) (platform, error) {
	if err := procSendInput.Find(); err != nil {
		return nil, fmt.Errorf("SendInput unavailable: %w", err)
	}
	p := &winPlatform{jobs: make(chan func(), 512), service: service, cursors: map[uintptr]string{}}
	ready := make(chan struct{})
	go p.thread(ready)
	<-ready
	return p, nil
}

func (p *winPlatform) thread(ready chan struct{}) {
	runtime.LockOSThread() // never unlocked: this thread belongs to us
	if procSetProcessDpiAwarenessCtx.Find() == nil {
		procSetProcessDpiAwarenessCtx.Call(^uintptr(3)) // PER_MONITOR_AWARE_V2 (-4)
	} else if procSetProcessDPIAware.Find() == nil {
		procSetProcessDPIAware.Call()
	}
	p.syncDesktop()
	for id, css := range standardCursors {
		if h, _, _ := procLoadCursor.Call(0, id); h != 0 {
			p.cursors[h] = css
		}
	}
	close(ready)
	for job := range p.jobs {
		job()
	}
}

// async queues fn on the UI thread (input injection: order preserved).
func (p *winPlatform) async(fn func()) {
	select {
	case p.jobs <- fn:
	default:
		log.Printf("input queue full, dropping event")
	}
}

// sync runs fn on the UI thread and waits for it.
func (p *winPlatform) sync(fn func()) {
	done := make(chan struct{})
	p.jobs <- func() { fn(); close(done) }
	<-done
}

// syncDesktop switches the UI thread to the current input desktop.
func (p *winPlatform) syncDesktop() {
	h, _, _ := procOpenInputDesktop.Call(0, 0, genericAll)
	if h == 0 {
		return // e.g. secure desktop while not running as SYSTEM
	}
	var buf [64]uint16
	var needed uint32
	procGetUserObjectInformation.Call(h, uoiName, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2), uintptr(unsafe.Pointer(&needed)))
	name := windows.UTF16ToString(buf[:])
	if name == p.deskName {
		procCloseDesktop.Call(h)
		return
	}
	if r, _, err := procSetThreadDesktop.Call(h); r == 0 {
		log.Printf("SetThreadDesktop(%s): %v", name, err)
		procCloseDesktop.Call(h)
		return
	}
	if p.desk != 0 {
		procCloseDesktop.Call(p.desk)
	}
	p.desk, p.deskName = h, name
}

func send(in *winInput) {
	procSendInput.Call(1, uintptr(unsafe.Pointer(in)), unsafe.Sizeof(*in))
}

func sendMouse(dx, dy int32, data, flags uint32) {
	in := winInput{typ: inputMouse, mi: mouseInput{dx: dx, dy: dy, mouseData: data, flags: flags}}
	send(&in)
}

func metric(i uintptr) int32 {
	r, _, _ := procGetSystemMetrics.Call(i)
	return int32(r)
}

// Monitor enumeration state. Only touched on the UI thread; the callback
// is created once because Windows limits the number of Go callbacks.
var (
	enumPrimary rect
	enumOthers  []rect
	enumCB      = syscall.NewCallback(func(hMon, hdc, lprc, lparam uintptr) uintptr {
		mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
		procGetMonitorInfo.Call(hMon, uintptr(unsafe.Pointer(&mi)))
		if mi.Flags&1 != 0 { // MONITORINFOF_PRIMARY
			enumPrimary = mi.Monitor
		} else {
			enumOthers = append(enumOthers, mi.Monitor)
		}
		return 1
	})
)

// displayRect returns the rectangle of display n: 0 = primary monitor,
// 1.. = the other monitors in enumeration order.
func displayRect(n int) (rect, bool) {
	enumPrimary, enumOthers = rect{}, nil
	procEnumDisplayMonitors.Call(0, 0, enumCB, 0)
	if n == 0 {
		return enumPrimary, enumPrimary.Right > enumPrimary.Left
	}
	if n-1 < len(enumOthers) {
		return enumOthers[n-1], true
	}
	return rect{}, false
}

var monitorCache struct {
	rects map[int]rect
	when  time.Time
}

func (p *winPlatform) moveAbs(m Mapping, x, y float64) {
	p.async(func() {
		flags := uint32(mouseMove | mouseAbsolute | mouseNoCoalesce | mouseVirtualDesk)
		vx, vy := float64(metric(smXVirtualScreen)), float64(metric(smYVirtualScreen))
		vw, vh := float64(metric(smCXVirtualScreen)), float64(metric(smCYVirtualScreen))
		if vw < 2 || vh < 2 {
			return
		}
		px, py := vx+x*(vw-1), vy+y*(vh-1)
		if !m.VirtualDesktop {
			// Monitor geometry is cached for a second: enumerating per
			// mouse move would be wasteful.
			c := &monitorCache
			if c.rects == nil || time.Since(c.when) > time.Second {
				c.rects, c.when = map[int]rect{}, time.Now()
			}
			r, ok := c.rects[m.Display]
			if !ok {
				r, ok = displayRect(m.Display)
				if ok {
					c.rects[m.Display] = r
				}
			}
			if ok {
				px = float64(r.Left) + x*float64(r.Right-r.Left-1)
				py = float64(r.Top) + y*float64(r.Bottom-r.Top-1)
			}
		}
		nx := (px - vx) * 65535 / (vw - 1)
		ny := (py - vy) * 65535 / (vh - 1)
		sendMouse(int32(math.Round(nx)), int32(math.Round(ny)), 0, flags)
	})
}

func (p *winPlatform) moveRel(dx, dy int) {
	p.async(func() { sendMouse(int32(dx), int32(dy), 0, mouseMove|mouseNoCoalesce) })
}

func (p *winPlatform) button(b int, down bool) {
	var flags, data uint32
	switch b {
	case 0:
		flags = pick(down, mouseLeftDown, mouseLeftUp)
	case 1:
		flags = pick(down, mouseMiddleDown, mouseMiddleUp)
	case 2:
		flags = pick(down, mouseRightDown, mouseRightUp)
	case 3:
		flags, data = pick(down, mouseXDown, mouseXUp), 1 // XBUTTON1 (back)
	case 4:
		flags, data = pick(down, mouseXDown, mouseXUp), 2 // XBUTTON2 (forward)
	default:
		return
	}
	p.async(func() { sendMouse(0, 0, data, flags) })
}

func (p *winPlatform) wheel(dx, dy int) {
	p.async(func() {
		if dy != 0 {
			sendMouse(0, 0, uint32(int32(dy)), mouseWheel)
		}
		if dx != 0 {
			sendMouse(0, 0, uint32(int32(dx)), mouseHWheel)
		}
	})
}

func (p *winPlatform) key(code string, down bool) {
	def := keymap[code]
	p.async(func() {
		in := winInput{typ: inputKeyboard}
		kb := (*keybdInput)(unsafe.Pointer(&in.mi))
		if def.win != 0 {
			kb.scan = def.win & 0xFF
			kb.flags = keyScancode
			if def.win&0xFF00 == 0xE000 {
				kb.flags |= keyExtended
			}
		} else {
			kb.vk = def.winVK
			if def.winVK == 0x90 { // NumLock is an extended key
				kb.flags |= keyExtended
			}
		}
		if !down {
			kb.flags |= keyUp
		}
		send(&in)
	})
}

func (p *winPlatform) cursor() string {
	css := ""
	p.sync(func() {
		ci := cursorInfo{Size: uint32(unsafe.Sizeof(cursorInfo{}))}
		if r, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); r == 0 {
			css = "default"
			return
		}
		if ci.Flags&cursorShowing == 0 || ci.Cursor == 0 {
			css = "none"
			return
		}
		if c, ok := p.cursors[ci.Cursor]; ok {
			css = c
		} else {
			css = "default"
		}
	})
	return css
}

func (p *winPlatform) desktop() string {
	name := ""
	p.sync(func() {
		p.syncDesktop()
		name = p.deskName
	})
	return name
}

func openClipboard() bool {
	for i := 0; i < 5; i++ {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func (p *winPlatform) getClipboard() (string, bool) {
	text, ok := "", true
	p.sync(func() {
		seq, _, _ := procGetClipboardSequenceNum.Call()
		if seq == p.clipSeq {
			text = p.clipText
			return
		}
		if r, _, _ := procIsClipboardFormatAvail.Call(cfUnicodeText); r == 0 {
			p.clipSeq, p.clipText = seq, ""
			text = ""
			return
		}
		if !openClipboard() {
			ok = false
			return
		}
		defer procCloseClipboard.Call()
		h, _, _ := procGetClipboardData.Call(cfUnicodeText)
		if h == 0 {
			ok = false
			return
		}
		ptr, _, _ := procGlobalLock.Call(h)
		if ptr == 0 {
			ok = false
			return
		}
		text = windows.UTF16PtrToString((*uint16)(foreignPtr(ptr)))
		procGlobalUnlock.Call(h)
		p.clipSeq, p.clipText = seq, text
	})
	return text, ok
}

func (p *winPlatform) setClipboard(text string) {
	p.sync(func() {
		u, err := windows.UTF16FromString(text)
		if err != nil {
			return
		}
		if !openClipboard() {
			return
		}
		defer procCloseClipboard.Call()
		procEmptyClipboard.Call()
		size := uintptr(len(u) * 2)
		h, _, _ := procGlobalAlloc.Call(gmemMoveable, size)
		if h == 0 {
			return
		}
		ptr, _, _ := procGlobalLock.Call(h)
		if ptr == 0 {
			procGlobalFree.Call(h)
			return
		}
		copy(unsafe.Slice((*uint16)(foreignPtr(ptr)), len(u)), u)
		procGlobalUnlock.Call(h)
		if r, _, _ := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
			procGlobalFree.Call(h)
			return
		}
		seq, _, _ := procGetClipboardSequenceNum.Call()
		p.clipSeq, p.clipText = seq, text
	})
}

func (p *winPlatform) keepAwake(on bool) {
	p.async(func() {
		if on {
			procSetThreadExecutionState.Call(esContinuous | esSystemRequired | esDisplayRequired)
		} else {
			procSetThreadExecutionState.Call(esContinuous)
		}
	})
}

// sendSAS asks the Remotly service (session 0) to generate Ctrl+Alt+Del:
// only a service may call SendSAS.
func (p *winPlatform) sendSAS() {
	name, _ := windows.UTF16PtrFromString(`Global\RemotlySAS`)
	h, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
	if err != nil {
		log.Printf("Ctrl+Alt+Del needs the Remotly service (agent installed with admin rights): %v", err)
		return
	}
	windows.SetEvent(h)
	windows.CloseHandle(h)
}

// foreignPtr converts an address of memory owned by Windows (not the Go
// heap) to a pointer.
func foreignPtr(p uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&p))
}

func (p *winPlatform) displays() int {
	n := 1
	p.sync(func() {
		displayRect(0)
		n = len(enumOthers)
		if enumPrimary.Right > enumPrimary.Left {
			n++
		}
	})
	return max(n, 1)
}

func pick(cond bool, a, b uint32) uint32 {
	if cond {
		return a
	}
	return b
}
