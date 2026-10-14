package winsvc

import (
	"errors"
	"io"
	"os"
	"syscall"
	"unsafe"

	"github.com/maxhummler-oss/remotly/internal/agent/stream"
	"golang.org/x/sys/windows"
)

// DesktopLauncher starts ffmpeg on the given window-station desktop
// ("Default", "Winlogon", …). Desktop Duplication only captures the desktop
// its process is attached to, so after a switch to the lock or logon screen
// ffmpeg must be restarted on the new desktop. os/exec cannot set the
// desktop, hence the manual CreateProcess.
func DesktopLauncher(desktop func() string) stream.Launcher {
	return func(path string, args []string, withStdin bool) (stream.Process, error) {
		name := desktop()
		if name == "" {
			name = "Default"
		}
		return startOnDesktop(`winsta0\`+name, path, args, withStdin)
	}
}

type deskProcess struct {
	handle         windows.Handle
	stdout, stderr *os.File
	stdin          *os.File
}

func (p *deskProcess) Stdout() io.Reader { return p.stdout }
func (p *deskProcess) Stderr() io.Reader { return p.stderr }
func (p *deskProcess) Stdin() io.WriteCloser {
	if p.stdin == nil {
		return nil
	}
	return p.stdin
}

func (p *deskProcess) Kill() { windows.TerminateProcess(p.handle, 1) }

func (p *deskProcess) Wait() error {
	windows.WaitForSingleObject(p.handle, windows.INFINITE)
	var code uint32
	windows.GetExitCodeProcess(p.handle, &code)
	windows.CloseHandle(p.handle)
	p.stdout.Close()
	p.stderr.Close()
	if p.stdin != nil {
		p.stdin.Close()
	}
	if code != 0 {
		return errors.New("exit status " + itoa(int(code)))
	}
	return nil
}

func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// pipe returns (parentEnd, childEnd); only the child end is inheritable.
func pipe(childReads bool) (*os.File, windows.Handle, error) {
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	var r, w windows.Handle
	if err := windows.CreatePipe(&r, &w, sa, 1<<20); err != nil {
		return nil, 0, err
	}
	parent, child := r, w
	if childReads {
		parent, child = w, r
	}
	windows.SetHandleInformation(parent, windows.HANDLE_FLAG_INHERIT, 0)
	return os.NewFile(uintptr(parent), "pipe"), child, nil
}

func startOnDesktop(desktop, path string, args []string, withStdin bool) (stream.Process, error) {
	stdout, childOut, err := pipe(false)
	if err != nil {
		return nil, err
	}
	stderr, childErr, err := pipe(false)
	if err != nil {
		stdout.Close()
		windows.CloseHandle(childOut)
		return nil, err
	}
	var stdin *os.File
	var childIn windows.Handle
	if withStdin {
		if stdin, childIn, err = pipe(true); err != nil {
			stdout.Close()
			stderr.Close()
			windows.CloseHandle(childOut)
			windows.CloseHandle(childErr)
			return nil, err
		}
	} else {
		childIn, _ = windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	}

	desk, _ := windows.UTF16PtrFromString(desktop)
	cmdLine, _ := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{path}, args...)))
	appName, _ := windows.UTF16PtrFromString(path)
	si := &windows.StartupInfo{
		Cb:         uint32(unsafe.Sizeof(windows.StartupInfo{})),
		Desktop:    desk,
		Flags:      windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW,
		ShowWindow: windows.SW_HIDE,
		StdInput:   childIn,
		StdOutput:  childOut,
		StdErr:     childErr,
	}
	var pi windows.ProcessInformation

	// Hold ForkLock so os/exec cannot leak our inheritable handles into
	// unrelated child processes started concurrently.
	syscall.ForkLock.Lock()
	err = windows.CreateProcess(appName, cmdLine, nil, nil, true, windows.CREATE_NO_WINDOW, nil, nil, si, &pi)
	syscall.ForkLock.Unlock()

	windows.CloseHandle(childOut)
	windows.CloseHandle(childErr)
	if withStdin {
		windows.CloseHandle(childIn)
	}
	if err != nil {
		stdout.Close()
		stderr.Close()
		if stdin != nil {
			stdin.Close()
		}
		return nil, err
	}
	windows.CloseHandle(pi.Thread)
	return &deskProcess{handle: pi.Process, stdout: stdout, stderr: stderr, stdin: stdin}, nil
}
