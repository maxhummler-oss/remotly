//go:build !windows

// Package winsvc runs the agent as a Windows service (Windows only).
package winsvc

import (
	"errors"

	"github.com/maxhummler-oss/remotly/internal/agent/stream"
)

var errNotWindows = errors.New("only available on Windows")

func Install() error       { return errNotWindows }
func Uninstall() error     { return errNotWindows }
func IsService() bool      { return false }
func Run(_ []string) error { return errNotWindows }

func DesktopLauncher(func() string) stream.Launcher { return stream.ExecLauncher }
