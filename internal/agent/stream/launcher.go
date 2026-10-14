package stream

import (
	"io"
	"os/exec"
)

// Process is a running ffmpeg instance.
type Process interface {
	Stdout() io.Reader
	Stderr() io.Reader
	Stdin() io.WriteCloser // nil unless requested
	Wait() error
	Kill()
}

// Launcher starts a process. withStdin requests a stdin pipe.
type Launcher func(path string, args []string, withStdin bool) (Process, error)

type execProcess struct {
	cmd            *exec.Cmd
	stdout, stderr io.Reader
	stdin          io.WriteCloser
}

func (p *execProcess) Stdout() io.Reader     { return p.stdout }
func (p *execProcess) Stderr() io.Reader     { return p.stderr }
func (p *execProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *execProcess) Wait() error           { return p.cmd.Wait() }
func (p *execProcess) Kill() {
	if p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
}

// ExecLauncher starts a regular child process.
func ExecLauncher(path string, args []string, withStdin bool) (Process, error) {
	cmd := exec.Command(path, args...)
	hideWindow(cmd)
	p := &execProcess{cmd: cmd}
	var err error
	if p.stdout, err = cmd.StdoutPipe(); err != nil {
		return nil, err
	}
	if p.stderr, err = cmd.StderrPipe(); err != nil {
		return nil, err
	}
	if withStdin {
		if p.stdin, err = cmd.StdinPipe(); err != nil {
			return nil, err
		}
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return p, nil
}
