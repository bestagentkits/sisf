package install

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// Cmd describes a program to run.
type Cmd struct {
	Name string
	Args []string
	// Dir is the working directory; empty means the caller's.
	Dir string
	// Env is added to the environment of the program, as KEY=VALUE.
	Env []string
	// OnlyEnv starts the program with Env and a minimal PATH and HOME, not
	// with the environment of the caller, so a secret of the caller does
	// not reach a program that was not given it.
	OnlyEnv bool
	// Stdout and Stderr receive the output of a background process; Run
	// captures output itself.
	Stdout, Stderr io.Writer
}

// Process is a program started in the background.
type Process interface {
	// Wait returns when the program exits.
	Wait() error
	// Terminate asks the program to stop; Kill stops it at once.
	Terminate() error
	Kill() error
}

// Exec runs programs. The real implementation is OSExec.
type Exec interface {
	// Run runs a program to completion and returns its standard output.
	// A failure carries the tail of the standard error, which holds no
	// secret because no secret is ever put on a command line.
	Run(ctx context.Context, c Cmd) ([]byte, error)
	// Start launches a program in the background.
	Start(ctx context.Context, c Cmd) (Process, error)
}

// OSExec runs programs on this machine.
type OSExec struct{}

// stderrTail is how much of a failed program's error output is reported.
const stderrTail = 2048

func (OSExec) Run(ctx context.Context, c Cmd) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir, cmd.Env = c.Dir, append(os.Environ(), c.Env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		tail := stderr.Bytes()
		if len(tail) > stderrTail {
			tail = tail[len(tail)-stderrTail:]
		}
		return stdout.Bytes(), fmt.Errorf("%s %s: %w: %s", c.Name, firstArg(c.Args), err, bytes.TrimSpace(tail))
	}
	return stdout.Bytes(), nil
}

func firstArg(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

// osProcess is a background program of this machine.
type osProcess struct{ cmd *exec.Cmd }

func (OSExec) Start(_ context.Context, c Cmd) (Process, error) {
	// The process is not bound to the caller's context: the group stops its
	// programs in order, with a grace period, rather than killing them all
	// the moment the context ends.
	cmd := exec.Command(c.Name, c.Args...)
	cmd.Dir, cmd.Env = c.Dir, append(os.Environ(), c.Env...)
	if c.OnlyEnv {
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}, c.Env...)
	}
	cmd.Stdout, cmd.Stderr = c.Stdout, c.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &osProcess{cmd: cmd}, nil
}

func (p *osProcess) Wait() error      { return p.cmd.Wait() }
func (p *osProcess) Terminate() error { return signalGroup(p.cmd, syscall.SIGTERM) }
func (p *osProcess) Kill() error      { return signalGroup(p.cmd, syscall.SIGKILL) }

// signalGroup signals the program's whole process group, so a program that
// started helpers does not leave them behind.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

// gracePeriod is how long a program gets to stop after SIGTERM.
const gracePeriod = 10 * time.Second

// How long Stop waits for the foreground process to end: stopWaitPolls polls,
// stopPoll apart, which covers the grace period of every unit.
const (
	stopPoll      = 500 * time.Millisecond
	stopWaitPolls = 80
)
