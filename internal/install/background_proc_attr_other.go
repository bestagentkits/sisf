//go:build !linux

package install

import "syscall"

// backgroundProcAttr starts a background program as the leader of a process
// group of its own, so it and the helpers it starts are signalled together.
// This system cannot tie a process to its parent's death; a unit that
// outlives `sisf up` still holds the run lock and is recorded, so
// `sisf stop` finds it.
func backgroundProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
