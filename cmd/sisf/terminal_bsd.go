//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package main

import "syscall"

// ioctlReadTermios reads a terminal's settings on the BSDs and macOS.
const ioctlReadTermios = syscall.TIOCGETA
