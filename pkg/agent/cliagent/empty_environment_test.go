//go:build unix

package cliagent_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest"
	"github.com/bestagentkits/sisf/pkg/core"
)

// A backend given no environment starts its tool with an empty one: the
// tool never inherits the secrets of the process that starts it, and a
// bare program name is not looked up on that process's PATH either.
func TestAnEmptyEnvironmentIsEmptyAndNeverTheParents(t *testing.T) {
	// Not parallel: it sets this process's environment.
	t.Setenv("SISF_STATE_DSN", "postgres://dispatcher:secret@db/sisf")
	t.Setenv("PARENT_ONLY_MARKER", "leaked")
	record := t.TempDir()
	bin := clitest.Install(t, "stub", clitest.Script{Record: record, Steps: []clitest.Step{{Emit: `{"type":"done"}`}}})

	bare, err := cliagent.New(cliagent.Config{Protocol: lineProtocol{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bare.Capabilities(context.Background()); !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("a bare name was found without a PATH of its own: %v", err)
	}

	b, err := cliagent.New(cliagent.Config{Protocol: lineProtocol{}, Binary: filepath.Join(bin, "stub"), ArtifactDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := b.Start(context.Background(), newTask("run-0001", "k1"), agent.WorkspaceRef{ID: "ws", Path: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if status, err := b.Wait(ctx, ref); err != nil || status.Phase != agent.PhaseSucceeded {
		t.Fatalf("run: %+v, %v", status, err)
	}
	for _, kv := range clitest.Recorded(t, record).Env {
		if strings.HasPrefix(kv, "SISF_STATE_DSN=") || strings.HasPrefix(kv, "PARENT_ONLY_MARKER=") || strings.HasPrefix(kv, "HOME=") {
			t.Errorf("the tool inherited %s", kv)
		}
	}
}
