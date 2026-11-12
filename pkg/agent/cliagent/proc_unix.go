//go:build unix

package cliagent

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// isolate puts the child in its own process group, so cancelling it reaches
// every process the agent started.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup kills the child's whole process group. The caller guarantees the
// child has not been reaped, so the group id cannot belong to anyone else.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

// readReady reads what the pipe f holds right now, without waiting: data,
// io.EOF once every writer closed it, or nothing with known set when it is
// open and empty. known is false when that cannot be told.
func readReady(f *os.File, p []byte) (n int, known bool, err error) {
	// A deadline that has passed refuses the read before it is tried.
	if f.SetReadDeadline(time.Time{}) != nil {
		return 0, false, nil
	}
	conn, err := f.SyscallConn()
	if err != nil {
		return 0, false, nil
	}
	var readErr error
	if err := conn.Read(func(fd uintptr) bool {
		n, readErr = syscall.Read(int(fd), p)
		return true
	}); err != nil {
		return 0, false, nil
	}
	switch {
	case errors.Is(readErr, syscall.EAGAIN):
		return 0, true, nil
	case readErr != nil:
		return 0, false, nil
	case n == 0 && len(p) > 0:
		return 0, true, io.EOF
	}
	return n, true, nil
}
