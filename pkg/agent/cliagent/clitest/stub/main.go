//go:build unix

// Command stub is the stand-in agent CLI that package clitest builds for
// tests. It is not meant to be run by hand.
package main

import (
	"os"

	"github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest"
)

func main() { os.Exit(clitest.Main()) }
