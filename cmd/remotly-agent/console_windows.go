package main

import (
	"log"
	"os"
	"syscall"
)

// The Windows agent is built as a GUI program (-H windowsgui) so no console
// window pops up when it starts at logon. When launched from a terminal we
// attach to that terminal so setup/probe output is still visible.
func attachConsole() {
	const attachParentProcess = ^uintptr(0) // (DWORD)-1
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	r, _, _ := kernel32.NewProc("AttachConsole").Call(attachParentProcess)
	if r == 0 {
		return
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = f
		os.Stderr = f
		log.SetOutput(f)
	}
}
