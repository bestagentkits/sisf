package wiring

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
)

// startRecorder records the task each Start receives.
type startRecorder struct {
	agent.Backend
	got agent.Task
}

func (r *startRecorder) Start(_ context.Context, task agent.Task, _ agent.WorkspaceRef, _ []string) (agent.RunRef, error) {
	r.got = task
	return agent.RunRef{}, nil
}

func TestTheModelOverrideReplacesOnlyTheRoutesModel(t *testing.T) {
	t.Parallel()
	rec := &startRecorder{}
	b, err := withModel(rec, "claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("withModel: %v", err)
	}
	task := agent.Task{Policy: core.PolicyRefs{ModelRoute: core.ModelRoute{Provider: "anthropic", Model: "mid", Effort: "high"}}}
	if _, err := b.Start(context.Background(), task, agent.WorkspaceRef{}, nil); err != nil {
		t.Fatal(err)
	}
	route := rec.got.Policy.ModelRoute
	if route.Model != "claude-sonnet-4-5" || route.Provider != "anthropic" || route.Effort != "high" {
		t.Fatalf("route = %+v, want the model replaced and the rest kept", route)
	}
	if task.Policy.ModelRoute.Model != "mid" {
		t.Fatal("the caller's task was changed")
	}
	if _, err := withModel(rec, "--dangerously-skip-permissions"); err == nil {
		t.Fatal("a model name that is a flag was accepted")
	}
}

func TestTheScriptedAgentRefusesAModel(t *testing.T) {
	t.Parallel()
	if _, _, err := newCodingAgent(AgentZoneConfig{Agent: "fake", Model: "claude-sonnet-4-5"}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("the scripted agent with a model: %v, want INVALID_ARGUMENT", err)
	}
}
