package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Helpers that bring a fake-backed Standalone install up and keep it running.

// standaloneUnitCount is supervisor, app, control, dispatcher, edge and the
// fake GitHub of the simulation profile.
const standaloneUnitCount = 6

// buildAnswers makes the fake "go build" write a distinct file and the fake
// docker answer like a daemon that built and measured everything.
func buildAnswers(c Cmd) ([]byte, error) {
	digest := func(s string) []byte {
		sum := sha256.Sum256([]byte(s))
		return []byte("sha256:" + hex.EncodeToString(sum[:]) + "\n")
	}
	switch {
	case c.Name == "go":
		out := c.Args[slices.Index(c.Args, "-o")+1]
		return nil, os.WriteFile(out, []byte("program "+out), 0o755)
	case c.Name == "docker" && c.Args[0] == "image":
		return digest(c.Args[len(c.Args)-1]), nil
	case c.Name == "docker" && c.Args[0] == "run":
		return digest("supervisor executable"), nil
	}
	return nil, nil
}

// standaloneRun is a Standalone install brought up by Up and still running.
type standaloneRun struct {
	in     *Installer
	exec   *fakeExec
	procs  []*fakeProc
	cancel context.CancelFunc
	done   chan error
}

func startStandalone(t *testing.T, mutate func(*UpOptions)) *standaloneRun {
	t.Helper()
	in, exec, _ := newTestInstaller(t)
	exec.answer = buildAnswers
	o := UpOptions{Init: initOptions(in, manifest.ModeStandalone)}
	if mutate != nil {
		mutate(&o)
	}
	ctx, cancel := context.WithCancel(t.Context())
	r := &standaloneRun{in: in, exec: exec, cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- in.Up(ctx, o) }()
	for range standaloneUnitCount {
		select {
		case p := <-exec.startCh:
			r.procs = append(r.procs, p)
		case err := <-r.done:
			t.Fatalf("Up returned before every unit started: %v", err)
		}
	}
	return r
}

func (r *standaloneRun) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	if err := <-r.done; err != nil {
		t.Fatalf("Up after cancel: %v", err)
	}
}

func (r *standaloneRun) proc(t *testing.T, base string) *fakeProc {
	t.Helper()
	for _, p := range r.procs {
		if strings.HasSuffix(p.cmd.Name, "/"+base) && (base != "factoryd" || len(p.cmd.Args) == 0) {
			return p
		}
	}
	t.Fatalf("no %s was started", base)
	return nil
}

// factoryd returns the control or dispatcher process of factoryd.
func (r *standaloneRun) factoryd(t *testing.T, role string) *fakeProc {
	t.Helper()
	for _, p := range r.procs {
		if strings.HasSuffix(p.cmd.Name, "/factoryd") && p.cmd.Args[0] == role {
			return p
		}
	}
	t.Fatalf("no factoryd %s was started", role)
	return nil
}
