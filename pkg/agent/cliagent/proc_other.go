//go:build !unix

package cliagent

import (
	"os"
	"os/exec"
)

// isolate does nothing where process groups are not available.
func isolate(*exec.Cmd) {}

// killGroup kills the child itself; descendants cannot be reached here.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// readReady cannot read a pipe without waiting here; a straggler's pipe is
// given up on when its wait runs out.
func readReady(*os.File, []byte) (int, bool, error) { return 0, false, nil }
