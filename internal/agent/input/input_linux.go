package input

import (
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// Linux backend (X11) using xdotool. Commands run on a worker goroutine;
// pending absolute moves are coalesced so a burst of motion never queues up
// behind slow process launches.

type linuxBackend struct {
	mu       sync.Mutex
	cmds     chan []string
	moves    chan [2]float64
	wheelAcc [2]int // accumulated sub-notch wheel delta
}

func newPlatform(bool) (platform, error) {
	if _, err := exec.LookPath("xdotool"); err != nil {
		return nil, fmt.Errorf("xdotool not found: install it (e.g. apt install xdotool)")
	}
	b := &linuxBackend{cmds: make(chan []string, 256), moves: make(chan [2]float64, 1)}
	go b.worker()
	return b, nil
}

func (l *linuxBackend) worker() {
	for {
		select {
		case args := <-l.cmds:
			// Apply the latest pending move first so clicks land correctly.
			select {
			case mv := <-l.moves:
				l.moveNow(mv)
			default:
			}
			l.run(args)
		case mv := <-l.moves:
			l.moveNow(mv)
		}
	}
}

// moveNow resolves normalized coordinates against the screen size.
func (l *linuxBackend) moveNow(mv [2]float64) {
	w, h := screenSize()
	l.run([]string{"mousemove", strconv.Itoa(int(mv[0] * float64(w-1))), strconv.Itoa(int(mv[1] * float64(h-1)))})
}

func (l *linuxBackend) run(args []string) {
	if out, err := exec.Command("xdotool", args...).CombinedOutput(); err != nil {
		log.Printf("xdotool %v: %v %s", args, err, out)
	}
}

var cachedW, cachedH int

func screenSize() (int, int) {
	if cachedW == 0 {
		out, err := exec.Command("xdotool", "getdisplaygeometry").Output()
		if err == nil {
			fmt.Sscanf(string(out), "%d %d", &cachedW, &cachedH)
		}
		if cachedW == 0 {
			cachedW, cachedH = 1920, 1080
		}
	}
	return cachedW, cachedH
}

func (l *linuxBackend) enqueue(args ...string) {
	select {
	case l.cmds <- args:
	default: // drop rather than block the data channel
	}
}

func (l *linuxBackend) moveAbs(_ Mapping, x, y float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.moves:
	default:
	}
	l.moves <- [2]float64{x, y}
}

func (l *linuxBackend) moveRel(dx, dy int) {
	l.enqueue("mousemove_relative", "--", strconv.Itoa(dx), strconv.Itoa(dy))
}

var xButtons = map[int]string{0: "1", 1: "2", 2: "3", 3: "8", 4: "9"}

func (l *linuxBackend) button(b int, down bool) {
	if down {
		l.enqueue("mousedown", xButtons[b])
	} else {
		l.enqueue("mouseup", xButtons[b])
	}
}

func (l *linuxBackend) wheel(dx, dy int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.wheelAcc[0] += dx
	l.wheelAcc[1] += dy
	for l.wheelAcc[1] >= 120 {
		l.enqueue("click", "4")
		l.wheelAcc[1] -= 120
	}
	for l.wheelAcc[1] <= -120 {
		l.enqueue("click", "5")
		l.wheelAcc[1] += 120
	}
	for l.wheelAcc[0] >= 120 {
		l.enqueue("click", "7")
		l.wheelAcc[0] -= 120
	}
	for l.wheelAcc[0] <= -120 {
		l.enqueue("click", "6")
		l.wheelAcc[0] += 120
	}
}

func (l *linuxBackend) key(code string, down bool) {
	sym := keymap[code].x11
	if sym == "" {
		return
	}
	if down {
		l.enqueue("keydown", sym)
	} else {
		l.enqueue("keyup", sym)
	}
}

func (l *linuxBackend) cursor() string  { return "" }
func (l *linuxBackend) desktop() string { return "" }

func (l *linuxBackend) getClipboard() (string, bool) {
	out, err := exec.Command("xclip", "-o", "-selection", "clipboard").Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

func (l *linuxBackend) setClipboard(text string) {
	cmd := exec.Command("xclip", "-i", "-selection", "clipboard")
	cmd.Stdin = strings.NewReader(text)
	cmd.Run()
}

func (l *linuxBackend) keepAwake(bool) {}
func (l *linuxBackend) sendSAS()       {}
func (l *linuxBackend) displays() int  { return 1 }
