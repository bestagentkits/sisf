//go:build unix

package cliagent

import (
	"os/exec"
	"syscall"
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
