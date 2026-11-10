//go:build unix

package wiring_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// The run controller records a coding run's spend only through the
// backend's agent.UsageReporter. The agent zone wraps the adapter to give
// each attempt a fresh workspace and, with --agent-model, to replace the
// route's model; neither wrapper may hide what the run consumed.
func TestTheCodingAgentsSpendIsReportedThroughTheAgentZonesWrappers(t *testing.T) {
	t.Parallel()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "..", "adapters", "agent", "claude", "testdata", "success.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for name, model := range map[string]string{"without a model override": "", "with a model override": "claude-sonnet-4-5"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bin := clitest.Install(t, "claude", clitest.Script{Steps: []clitest.Step{{AwaitStdin: "*EOF*"}, {EmitFile: fixture}}})
			env := dispatcherEnv(bin)
			source, base := gitSource(t)
			zone, err := wiring.NewAgentZone(wiring.AgentZoneConfig{
				Store: nopStore{}, Clock: core.SystemClock{}, Agent: "claude", Owner: "exporter@test", Model: model,
				WorkspaceRoot: t.TempDir(), WorkspaceSource: source, ArtifactDir: t.TempDir(),
				Manifest:  &manifest.Manifest{TrustedCodeOnly: true},
				LookupEnv: func(n string) (string, bool) { v, ok := env[n]; return v, ok },
			})
			if err != nil {
				t.Fatal(err)
			}
			reporter, ok := zone.Agent.(agent.UsageReporter)
			if !ok {
				t.Fatal("the agent zone's coding agent hides the adapter's usage reporter, so no coding spend is recorded")
			}
			ref, err := zone.Agent.Start(context.Background(), codingTask("claude"), agent.WorkspaceRef{ID: "run-0001", BaseSHA: base}, nil)
			if err != nil {
				t.Fatal(err)
			}
			waitForTheRunToEnd(t, zone.Agent, ref)
			usage, err := reporter.Usage(context.Background(), ref)
			if err != nil {
				t.Fatal(err)
			}
			if len(usage) != 1 || usage[0].InputTokens == 0 || usage[0].Cost == 0 {
				t.Fatalf("usage = %+v, want the run's tokens and cost", usage)
			}
			if model != "" && usage[0].ModelRoute.Model != model {
				t.Fatalf("usage is recorded on model %q, want the model the run ran on, %q", usage[0].ModelRoute.Model, model)
			}
		})
	}
}

// waitForTheRunToEnd polls the run until it is no longer queued or running.
func waitForTheRunToEnd(t *testing.T, b agent.Backend, ref agent.RunRef) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		status, err := b.Status(context.Background(), ref)
		if err != nil {
			t.Fatal(err)
		}
		if status.Phase != agent.PhaseRunning && status.Phase != agent.PhaseQueued {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the agent run did not end")
		}
	}
}
