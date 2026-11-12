package agent_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
)

const sha = core.CommitSHA("0123456789abcdef0123456789abcdef01234567")

func validTask() agent.Task {
	return agent.Task{
		RunID:   "run-0001",
		IssueID: core.IssueRef{Owner: "acme", Repo: "app", Number: 7},
		Role:    agent.RoleCoder,
		Step:    core.StepCoding,
		Handoff: agent.Handoff{SoW: "Add GET /version.", DoD: "GET /version returns the build revision."},
		Policy: core.PolicyRefs{PolicySHA: sha, ReviewSHA: sha, DecisionSHA: sha, FeatureCatalogSHA: sha,
			ModelRoute: core.ModelRoute{Provider: "anthropic", Model: "mid", Effort: "low"}},
		Limits:         agent.Limits{Timeout: 30 * time.Minute},
		IdempotencyKey: "run-0001:start",
	}
}

func TestTaskValidation(t *testing.T) {
	if err := validTask().Validate(); err != nil {
		t.Fatalf("valid task rejected: %v", err)
	}
	cases := map[string]func(*agent.Task){
		"no run":                func(k *agent.Task) { k.RunID = "" },
		"no issue":              func(k *agent.Task) { k.IssueID = core.IssueRef{} },
		"unknown role":          func(k *agent.Task) { k.Role = "owner" },
		"unknown step":          func(k *agent.Task) { k.Step = "advisor" },
		"no scope of work":      func(k *agent.Task) { k.Handoff.SoW = " " },
		"no definition of done": func(k *agent.Task) { k.Handoff.DoD = "" },
		"policy not pinned":     func(k *agent.Task) { k.Policy.PolicySHA = "main" },
		"no time limit":         func(k *agent.Task) { k.Limits.Timeout = 0 },
		"negative turn limit":   func(k *agent.Task) { k.Limits.MaxTurns = -1 },
		"no idempotency key":    func(k *agent.Task) { k.IdempotencyKey = "" },
	}
	for name, mutate := range cases {
		task := validTask()
		mutate(&task)
		if core.CodeOf(task.Validate()) != core.CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, task.Validate())
		}
	}
}

func TestOnlyFinishedPhasesAreTerminalAndAPauseIsNeverSuccess(t *testing.T) {
	terminal := map[agent.Phase]bool{agent.PhaseSucceeded: true, agent.PhaseFailed: true, agent.PhaseCancelled: true}
	for _, p := range agent.Phases() {
		if !p.Valid() {
			t.Errorf("phase %q is listed but not valid", p)
		}
		if p.Terminal() != terminal[p] {
			t.Errorf("phase %q terminal = %v", p, p.Terminal())
		}
	}
	if len(agent.Phases()) != 7 {
		t.Errorf("Phases() = %v", agent.Phases())
	}
	if agent.PhasePaused.Terminal() || agent.Phase("done").Valid() || agent.Phase("").Terminal() {
		t.Error("a paused or unknown phase must not read as finished")
	}
}

func TestRolesAndKindsAreClosedSets(t *testing.T) {
	if !agent.RoleCoder.Valid() || !agent.RoleQA.Valid() || agent.Role("").Valid() {
		t.Error("role validation is wrong")
	}
	for _, k := range []agent.EventKind{agent.EventMessage, agent.EventToolCall, agent.EventToolResult,
		agent.EventStatus, agent.EventUsage, agent.EventCheckpoint, agent.EventError} {
		if !k.Valid() {
			t.Errorf("event kind %q must be valid", k)
		}
	}
	if agent.EventKind("thought").Valid() {
		t.Error("unknown event kinds must be rejected")
	}
	for _, k := range []agent.ArtifactKind{agent.ArtifactPatch, agent.ArtifactTestReport, agent.ArtifactBuild,
		agent.ArtifactLog, agent.ArtifactTranscript} {
		if !k.Valid() {
			t.Errorf("artifact kind %q must be valid", k)
		}
	}
	if agent.ArtifactKind("secret").Valid() {
		t.Error("unknown artifact kinds must be rejected")
	}
}

// minimal implements only the required interface; full adds every optional one.
type minimal struct{}

func (minimal) Name() string { return "minimal" }
func (minimal) Capabilities(context.Context) (core.CapabilitySet, error) {
	return core.CapabilitySet{core.CapAgentCancel: core.SupportNotSupported}, nil
}
func (minimal) Start(context.Context, agent.Task, agent.WorkspaceRef, []string) (agent.RunRef, error) {
	return agent.RunRef{Backend: "minimal", ID: "provider-1", RunID: "run-0001"}, nil
}
func (minimal) Status(context.Context, agent.RunRef) (agent.Status, error) {
	return agent.Status{Phase: agent.PhaseRunning}, nil
}
func (minimal) Events(context.Context, agent.RunRef, agent.Cursor) (agent.EventPage, error) {
	return agent.EventPage{}, nil
}
func (minimal) Cancel(context.Context, agent.RunRef) error {
	return core.NewError(core.CodeNotSupported, "this backend cannot cancel a run")
}
func (minimal) CollectArtifacts(context.Context, agent.RunRef) ([]agent.Artifact, error) {
	return nil, nil
}

type full struct{ minimal }

func (full) Resume(context.Context, agent.RunRef, agent.Handoff) (agent.RunRef, error) {
	return agent.RunRef{}, nil
}
func (full) Checkpoint(context.Context, agent.RunRef) (string, error)  { return "cp-1", nil }
func (full) Usage(context.Context, agent.RunRef) ([]core.Usage, error) { return nil, nil }
func (full) HumanInput(context.Context, agent.RunRef, string) error    { return nil }
func (full) SessionLink(context.Context, agent.RunRef) (string, error) {
	return "https://example.test/s/1", nil
}
func (full) HandleWebhook(context.Context, http.Header, []byte) (agent.RunRef, []agent.Event, error) {
	return agent.RunRef{}, nil, nil
}

func TestOptionalAbilitiesAreSeparateInterfaces(t *testing.T) {
	var base agent.Backend = minimal{}
	if _, ok := base.(agent.Resumer); ok {
		t.Fatal("a backend without resume must not satisfy Resumer")
	}
	if err := base.Cancel(context.Background(), agent.RunRef{}); !core.HasCode(err, core.CodeNotSupported) {
		t.Fatalf("a missing ability must be refused as NOT_SUPPORTED, got %v", err)
	}
	var rich agent.Backend = full{}
	_, resumer := rich.(agent.Resumer)
	_, checkpointer := rich.(agent.Checkpointer)
	_, usage := rich.(agent.UsageReporter)
	_, input := rich.(agent.HumanInputReceiver)
	_, link := rich.(agent.SessionLinker)
	_, hooks := rich.(agent.WebhookHandler)
	if !resumer || !checkpointer || !usage || !input || !link || !hooks {
		t.Fatal("a backend offering every optional ability must satisfy each optional interface")
	}
}
