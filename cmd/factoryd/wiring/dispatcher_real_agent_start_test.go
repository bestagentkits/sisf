//go:build unix

package wiring_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// A real coding agent works in a workspace the agent zone checks out at a
// commit. The dispatcher must name that commit, the one the run's policy
// is pinned to and the supervisor builds the patch on; a workspace without
// one cannot be checked out, so the agent never starts and the issue stays
// Claimed. The scripted agent of the acceptance suite needs no checkout,
// which is why only a real install showed it.
func TestTheDispatcherStartsARealCodingAgentOnTheCommitItsPolicyPins(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		fixture, err := filepath.Abs(filepath.Join("..", "..", "..", "adapters", "agent", "claude", "testdata", "success.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		bin := clitest.Install(t, "claude", clitest.Script{Steps: []clitest.Step{{AwaitStdin: "*EOF*"}, {EmitFile: fixture}}})
		env := dispatcherEnv(bin)
		source, base := gitSource(t)
		store := db.Open(t, core.ZoneAgent)
		zone, err := wiring.NewAgentZone(wiring.AgentZoneConfig{
			Store: store, Clock: db.Clock, Agent: "claude", Owner: "exporter@test", Model: "claude-sonnet-4-5",
			WorkspaceRoot: t.TempDir(), WorkspaceSource: source, ArtifactDir: t.TempDir(),
			Manifest:  &manifest.Manifest{TrustedCodeOnly: true},
			LookupEnv: func(n string) (string, bool) { v, ok := env[n]; return v, ok },
		})
		if err != nil {
			t.Fatal(err)
		}
		confirmUnlimitedBudget(t, db)
		issue := readyIssue(t, db)
		d, err := dispatcher.New(dispatcher.Config{
			Owner: "dispatcher@test", State: store, Agent: zone.Agent, Clock: db.Clock,
			Budget: budget.NewGate(budget.Config{Confirmations: dispatcher.KVConfirmation{}, Clock: db.Clock}),
			Briefs: dispatcher.BriefFunc(func(context.Context, core.IssueRef) (dispatcher.Brief, error) {
				return briefAt(base), nil
			}),
			Limits: dispatcher.Limits{RunTimeout: time.Hour, MaxAttempts: 3, MaxConcurrent: 1},
		})
		if err != nil {
			t.Fatal(err)
		}

		res, err := d.Tick(ctx)
		if err != nil || len(res.Claimed) != 1 {
			t.Fatalf("the first tick claimed %v, error %v", res.Claimed, err)
		}
		record := issueRecord(t, store, issue)
		run, err := d.RunRecord(ctx, record.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if record.State != core.StateDeveloping || run.Agent == nil {
			t.Fatalf("the issue is %s and the agent did not start; failures %+v", record.State, run.Failures)
		}
		// The agent ran in the checked-out workspace and finished: the run
		// succeeds on a later tick.
		for deadline := time.Now().Add(30 * time.Second); len(res.Succeeded) == 0; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("the coding run did not succeed")
			}
			if res, err = d.Tick(ctx); err != nil || len(res.Errors) > 0 {
				t.Fatalf("tick: %v %v", err, res.Errors)
			}
		}
	})
}

// briefAt is a brief whose policy is pinned to commit.
func briefAt(commit core.CommitSHA) dispatcher.Brief {
	return dispatcher.Brief{
		Handoff: agent.Handoff{SoW: "Greet the world.", DoD: "- It greets the world."},
		Policy: core.PolicyRefs{PolicySHA: commit, ReviewSHA: commit, DecisionSHA: commit, FeatureCatalogSHA: commit,
			ModelRoute: core.ModelRoute{Provider: "anthropic", Model: "mid"}},
	}
}

// confirmUnlimitedBudget stores the Owner's budget confirmation through
// the control zone, as the installer does.
func confirmUnlimitedBudget(t *testing.T, db *sqlstoretest.Database) {
	t.Helper()
	raw, err := json.Marshal(dispatcher.BudgetConfirmation{
		Mode: core.BudgetUnlimited, AlertOff: true, ConfirmedAt: db.Clock.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.Open(t, core.ZoneControl).Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		_, err := tx.PutKV(ctx, state.KVEntry{Namespace: dispatcher.ConfirmationNamespace, Key: dispatcher.ConfirmationKey, Value: raw})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// readyIssue takes an issue through intake to Ready, as the control zone does.
func readyIssue(t *testing.T, db *sqlstoretest.Database) core.IssueRef {
	t.Helper()
	issue := core.IssueRef{Owner: "acme", Repo: "app", Number: 7}
	control := db.Open(t, core.ZoneControl)
	steps := []core.Transition{
		{IssueID: issue, To: core.StateIntake, Event: core.EventIssueReceived, IdempotencyKey: "received"},
		{IssueID: issue, From: core.StateIntake, Event: core.EventInfoSufficient, ExpectedVersion: 1, IdempotencyKey: "sufficient"},
		{IssueID: issue, From: core.StateScopeDraft, Event: core.EventScopePermitted, ExpectedVersion: 2, IdempotencyKey: "permitted"},
	}
	for _, step := range steps {
		if !step.Creates() {
			to, err := statemachine.Next(step.From, step.Event)
			if err != nil {
				t.Fatal(err)
			}
			step.To = to
		}
		err := control.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
			_, err := tx.Transition(ctx, step)
			return err
		})
		if err != nil {
			t.Fatalf("take %s to %s: %v", issue, step.To, err)
		}
	}
	return issue
}

// issueRecord reads the lifecycle record of issue.
func issueRecord(t *testing.T, store state.Store, issue core.IssueRef) core.StateRecord {
	t.Helper()
	var record core.StateRecord
	err := store.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		record, err = tx.GetRecord(ctx, issue)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
}
