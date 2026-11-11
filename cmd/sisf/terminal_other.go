//go:build !(darwin || freebsd || netbsd || openbsd || dragonfly || linux)

package main

import "os"

// isTerminalFile answers false where no terminal check is known: the Owner
// then confirms the budget with flags instead of a prompt.
func isTerminalFile(*os.File) bool { return false }
