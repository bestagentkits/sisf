//go:build linux

package main

import "syscall"

// ioctlReadTermios reads a terminal's settings on Linux.
const ioctlReadTermios = syscall.TCGETS
