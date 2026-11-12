package runcontrol_test

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/cmd/supervisor/runcontrol"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/internal/supervisor/killswitch"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// revocations records the runs whose grants were revoked.
type revocations struct{ runs []core.RunID }

func (r *revocations) RevokeRun(_ context.Context, run core.RunID) error {
	r.runs = append(r.runs, run)
	return nil
}

const token = "0123456789abcdef0123456789abcdef-admin"

// The supervisor's emergency stop cancels the agent run and revokes the
// run's secrets in the dispatcher's process, through its admin route.
func TestTheEmergencyStopCancelsRunsAndRevokesSecretsInTheDispatcher(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet", "Short.\n\n### Definition of Done\n- greets\n", github.LabelReady, github.LabelKindBug)
	if err := f.Agent.Script(agent.RoleCoder, f.Issue(number), fake.Hang()); err != nil {
		t.Fatal(err)
	}
	f.Pass()
	f.Tick()
	run := f.Record(number).RunID

	revoked := &revocations{}
	handler, err := runcontrol.Handler(token, f.Agent, revoked)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := runcontrol.NewClient(srv.URL, core.NewSecretValue([]byte(token)))
	if err != nil {
		t.Fatal(err)
	}
	sw, err := killswitch.New(killswitch.Config{FactoryID: f.Manifest.FactoryID, State: f.ControlStore, Clock: f.Clock, Runs: client, Secrets: client})
	if err != nil {
		t.Fatal(err)
	}
	report, err := sw.Stop(ctx, supervisor.EmergencyStopRequest{Actor: "owner:local", Reason: "drill"})
	if err != nil || len(report.Failures) != 0 {
		t.Fatalf("the stop failed: %+v, %v", report, err)
	}
	if !slices.Contains(report.RunsCancelled, run) || !slices.Contains(report.SecretsRevoked, run) || !slices.Contains(revoked.runs, run) {
		t.Fatalf("the run was not stopped: %+v, revoked %v", report, revoked.runs)
	}
	ref := f.Runs(number)[0].Agent
	if status, err := f.Agent.Status(ctx, *ref); err != nil || status.Phase != agent.PhaseCancelled {
		t.Fatalf("the agent run is %+v, %v", status, err)
	}

	// Without the token the route stops nothing.
	stranger, _ := runcontrol.NewClient(srv.URL, core.NewSecretValue([]byte("not-the-token-0123456789abcdef0123")))
	if err := stranger.RevokeRun(ctx, run); !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("a request without the token was served: %v", err)
	}
}
