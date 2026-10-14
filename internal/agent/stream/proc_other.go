//go:build !windows

package stream

import "os/exec"

func hideWindow(*exec.Cmd) {}
