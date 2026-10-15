// Package winsvc runs the agent as a Windows service.
//
// A service lives in session 0 and cannot see any desktop. The service
// therefore only supervises: it starts the actual agent ("worker") with its
// own SYSTEM token in the active console session, restarts it when it exits
// or the console session changes (logon, user switch), and performs
// Ctrl+Alt+Del on request, which only a service may do. Running as SYSTEM
// lets the worker follow the input desktop onto the lock screen, the logon
// screen after a reboot and UAC prompts.
package winsvc

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Name is the service name.
const Name = "RemotlyAgent"

const sasEventName = `Global\RemotlySAS`

// Install registers the service (needs administrator rights).
func Install() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	if s, err := m.OpenService(Name); err == nil {
		s.Close()
		return nil // already installed; binary path is unchanged
	}
	s, err := m.CreateService(Name, exe, mgr.Config{
		DisplayName:  "Remotly Agent",
		Description:  "Remotly Fernzugriff: Bildschirmübertragung und Eingabe für Remotly-Viewer.",
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
	}, "service", "run")
	if err != nil {
		return err
	}
	defer s.Close()
	s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 24*60*60)

	// Allow services to generate Ctrl+Alt+Del (SendSAS).
	if k, _, err := registry.CreateKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`, registry.SET_VALUE); err == nil {
		k.SetDWordValue("SoftwareSASGeneration", 1)
		k.Close()
	}
	fmt.Println("Dienst", Name, "installiert.")
	return nil
}

// Uninstall stops and removes the service.
func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return err
	}
	defer s.Close()
	s.Control(svc.Stop)
	time.Sleep(2 * time.Second)
	return s.Delete()
}

// IsService reports whether the process was started by the SCM.
func IsService() bool {
	ok, _ := svc.IsWindowsService()
	return ok
}

// Run runs the service; workerArgs are passed to the worker process.
func Run(workerArgs []string) error {
	return svc.Run(Name, &handler{args: workerArgs})
}

type handler struct {
	args []string
}

func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.supervise(stop)
	}()
	go sasListener(stop)
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for c := range req {
		switch c.Cmd {
		case svc.Interrogate:
			status <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending}
			close(stop)
			<-done
			return false, 0
		}
	}
	return false, 0
}

// supervise keeps one worker running in the active console session.
func (h *handler) supervise(stop chan struct{}) {
	var worker windows.Handle
	var workerSession uint32 = 0xFFFFFFFF
	kill := func() {
		if worker != 0 {
			windows.TerminateProcess(worker, 0)
			windows.WaitForSingleObject(worker, 5000)
			windows.CloseHandle(worker)
			worker = 0
		}
	}
	defer kill()

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	lastStart := time.Time{}
	exe, _ := os.Executable()
	exeInfo, _ := os.Stat(exe)
	for {
		// The worker replaced the binary (auto-update): restart the
		// service itself too, via the SCM's recovery actions.
		if info, err := os.Stat(exe); err == nil && exeInfo != nil && !info.ModTime().Equal(exeInfo.ModTime()) {
			log.Printf("agent binary updated, restarting service")
			kill()
			os.Exit(1)
		}
		session := windows.WTSGetActiveConsoleSessionId()
		alive := worker != 0
		if alive {
			if ev, _ := windows.WaitForSingleObject(worker, 0); ev == windows.WAIT_OBJECT_0 {
				var code uint32
				windows.GetExitCodeProcess(worker, &code)
				log.Printf("worker exited (code %d)", code)
				windows.CloseHandle(worker)
				worker, alive = 0, false
			}
		}
		if alive && session != workerSession {
			log.Printf("console session changed %d -> %d", workerSession, session)
			kill()
			alive = false
		}
		if !alive && session != 0xFFFFFFFF && time.Since(lastStart) > 3*time.Second {
			lastStart = time.Now()
			p, err := spawnWorker(session, h.args)
			if err != nil {
				log.Printf("start worker in session %d: %v", session, err)
			} else {
				worker, workerSession = p, session
			}
		}
		select {
		case <-stop:
			return
		case <-tick.C:
		}
	}
}

// spawnWorker starts the agent as SYSTEM inside the given session.
func spawnWorker(session uint32, args []string) (windows.Handle, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	var self windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ALL_ACCESS, &self); err != nil {
		return 0, fmt.Errorf("OpenProcessToken: %w", err)
	}
	defer self.Close()
	var tok windows.Token
	if err := windows.DuplicateTokenEx(self, windows.MAXIMUM_ALLOWED, nil, windows.SecurityImpersonation, windows.TokenPrimary, &tok); err != nil {
		return 0, fmt.Errorf("DuplicateTokenEx: %w", err)
	}
	defer tok.Close()
	if err := windows.SetTokenInformation(tok, windows.TokenSessionId, (*byte)(unsafe.Pointer(&session)), 4); err != nil {
		return 0, fmt.Errorf("SetTokenInformation: %w", err)
	}

	cmdLine, _ := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{exe}, args...)))
	appName, _ := windows.UTF16PtrFromString(exe)
	desk, _ := windows.UTF16PtrFromString(`winsta0\Default`)
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	si := &windows.StartupInfo{
		Cb:         uint32(unsafe.Sizeof(windows.StartupInfo{})),
		Desktop:    desk,
		Flags:      windows.STARTF_USESHOWWINDOW,
		ShowWindow: windows.SW_HIDE,
	}
	var pi windows.ProcessInformation
	if err := windows.CreateProcessAsUser(tok, appName, cmdLine, nil, nil, false,
		windows.CREATE_NO_WINDOW|windows.CREATE_UNICODE_ENVIRONMENT, nil, dir, si, &pi); err != nil {
		return 0, fmt.Errorf("CreateProcessAsUser: %w", err)
	}
	windows.CloseHandle(pi.Thread)
	log.Printf("worker started in session %d (pid %d)", session, pi.ProcessId)
	return pi.Process, nil
}

// sasListener performs Ctrl+Alt+Del when a worker signals the named event.
func sasListener(stop chan struct{}) {
	name, _ := windows.UTF16PtrFromString(sasEventName)
	ev, err := windows.CreateEvent(nil, 0, 0, name) // auto-reset
	if err != nil {
		log.Printf("SAS event: %v", err)
		return
	}
	defer windows.CloseHandle(ev)
	sas := windows.NewLazySystemDLL("sas.dll").NewProc("SendSAS")
	for {
		r, _ := windows.WaitForSingleObject(ev, 500)
		select {
		case <-stop:
			return
		default:
		}
		if r == windows.WAIT_OBJECT_0 {
			if err := sas.Find(); err != nil {
				log.Printf("SendSAS unavailable: %v", err)
				continue
			}
			sas.Call(0) // FALSE: called by a service
			log.Printf("Ctrl+Alt+Del sent")
		}
	}
}
