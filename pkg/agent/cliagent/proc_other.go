//go:build !unix

package cliagent

import "os/exec"

// isolate does nothing where process groups are not available.
func isolate(*exec.Cmd) {}

// killGroup kills the child itself; descendants cannot be reached here.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
