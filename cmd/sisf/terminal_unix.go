//go:build darwin || freebsd || netbsd || openbsd || dragonfly || linux

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// isTerminalFile reports whether f is a terminal: it answers the request
// for terminal settings. A character device that is not a terminal, such
// as /dev/null, does not.
func isTerminalFile(f *os.File) bool {
	var settings syscall.Termios
	// unsafe: the ioctl writes the settings into this struct, which is the
	// layout the kernel expects; nothing else is read through the pointer.
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(ioctlReadTermios), uintptr(unsafe.Pointer(&settings)))
	return errno == 0
}
