package runactions_test

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/internal/actions/runactions"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

func sampleWorld() world {
	return world{
		records: records{
			issueRef(1): record(1, core.StateBlocked, "", 1),
			issueRef(2): record(2, core.StateDeveloping, "run-2", 5),
			issueRef(3): record(3, core.StateRolledBack, "", 3),
		},
		runs:  runs{"run-2": {Issue: issueRef(2), RunID: "run-2", Status: dispatcher.RunWorking, Attempt: 1}},
		inbox: inbox{requests: []approvals.Request{{ID: "a", Status: approvals.StatusPending}, {ID: "b", Status: approvals.StatusConsumed}}},
	}
}

func TestFactoryStatusCountsIssuesStopsAndOpenApprovals(t *testing.T) {
	t.Parallel()
	got, err := call[runactions.FactoryStatus](t, sampleWorld().action(t, runactions.ActionFactoryStatus), ``)
	if err != nil {
		t.Fatal(err)
	}
	if got.IssueTotal != 3 || got.Stopped != 2 || got.OpenApprovals != 1 || len(got.IssueCounts) != len(core.States()) ||
		got.FactoryID != "acme-app" || !got.GeneratedAt.Equal(t0) {
		t.Fatalf("status = %+v", got)
	}
}

func TestIssuesListFiltersSortsAndLimits(t *testing.T) {
	t.Parallel()
	list := sampleWorld().action(t, runactions.ActionIssuesList)
	got, err := call[runactions.IssueList](t, list, `{"limit":2}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 3 || len(got.Issues) != 2 || got.Issues[0].IssueID != issueRef(2) || got.Issues[1].IssueID != issueRef(3) {
		t.Fatalf("list = %+v", got)
	}
	got, err = call[runactions.IssueList](t, list, `{"states":["Blocked"]}`)
	if err != nil || got.Total != 1 || got.Issues[0].State != core.StateBlocked {
		t.Fatalf("filtered = %+v, %v", got, err)
	}
	for _, in := range []string{`{"states":["Nope"]}`, `{"limit":501}`, `{"limit":-1}`} {
		if _, err := call[runactions.IssueList](t, list, in); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: error = %v", in, err)
		}
	}
}

func TestInspectReturnsRecordAndCurrentRun(t *testing.T) {
	t.Parallel()
	inspect := sampleWorld().action(t, runactions.ActionRunsInspect)
	got, err := call[runactions.Inspection](t, inspect, `{"issue":"acme/app#2"}`)
	if err != nil || got.Run == nil || got.Run.RunID != "run-2" || got.Record.State != core.StateDeveloping {
		t.Fatalf("inspection = %+v, %v", got, err)
	}
	got, err = call[runactions.Inspection](t, inspect, `{"issue":"acme/app#1"}`)
	if err != nil || got.Run != nil {
		t.Fatalf("an issue without a run must show no run: %+v, %v", got, err)
	}
}

func TestNewRefusesMissingDependencies(t *testing.T) {
	t.Parallel()
	if _, err := runactions.New(runactions.Deps{FactoryID: "acme-app"}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("error = %v", err)
	}
}

func TestStoreRecordsReadsTheOperationalStore(t *testing.T) {
	t.Parallel()
	store := intaketest.NewState(intaketest.NewClock(t0))
	err := store.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		_, err := tx.Transition(ctx, core.Transition{IssueID: issueRef(1), To: core.StateIntake,
			Event: core.EventIssueReceived, IdempotencyKey: "received", ActorID: "test"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r := runactions.StoreRecords(store)
	if got, err := r.Record(context.Background(), issueRef(1)); err != nil || got.State != core.StateIntake {
		t.Fatalf("record = %+v, %v", got, err)
	}
	if _, err := r.Record(context.Background(), issueRef(2)); !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("missing record = %v", err)
	}
	if all, err := r.Records(context.Background(), state.RecordFilter{}); err != nil || len(all) != 1 {
		t.Fatalf("records = %v, %v", all, err)
	}
}
