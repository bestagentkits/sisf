package runactions_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/actions/runactions"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

var t0 = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

type records map[core.IssueRef]core.StateRecord

func (r records) Record(_ context.Context, issue core.IssueRef) (core.StateRecord, error) {
	rec, ok := r[issue]
	if !ok {
		return core.StateRecord{}, core.NewError(core.CodeNotFound, "no record")
	}
	return rec, nil
}

func (r records) Records(_ context.Context, f state.RecordFilter) ([]core.StateRecord, error) {
	var out []core.StateRecord
	for _, rec := range r {
		if len(f.States) == 0 || slices.Contains(f.States, rec.State) {
			out = append(out, rec)
		}
	}
	return out, nil
}

type runs map[core.RunID]dispatcher.RunRecord

func (r runs) RunRecord(_ context.Context, id core.RunID) (dispatcher.RunRecord, error) {
	rec, ok := r[id]
	if !ok {
		return dispatcher.RunRecord{}, core.NewError(core.CodeNotFound, "no run")
	}
	return rec, nil
}

type inbox struct {
	requests  []approvals.Request
	decisions map[string]approvals.Approval
}

func (i inbox) List(_ context.Context, f approvals.Filter) ([]approvals.Request, error) {
	var out []approvals.Request
	for _, r := range i.requests {
		if (f.IssueID.IsZero() || r.IssueID == f.IssueID) && (len(f.Statuses) == 0 || slices.Contains(f.Statuses, r.Status)) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (i inbox) Decision(_ context.Context, id string) (approvals.Approval, error) {
	a, ok := i.decisions[id]
	if !ok {
		return approvals.Approval{}, core.NewError(core.CodeNotFound, "undecided")
	}
	return a, nil
}

func issueRef(n int) core.IssueRef { return core.IssueRef{Owner: "acme", Repo: "app", Number: n} }

func record(n int, st core.State, run core.RunID, minutes int) core.StateRecord {
	return core.StateRecord{FactoryID: "acme-app", IssueID: issueRef(n), State: st, Version: 3, IdempotencyKey: "k",
		Attempt: 1, RunID: run, UpdatedAt: t0.Add(time.Duration(minutes) * time.Minute)}
}

type world struct {
	records  records
	runs     runs
	inbox    inbox
	holds    runactions.QAHolds
	releases runactions.ReleaseHolds
}

func (w world) action(t *testing.T, name string) core.Action {
	t.Helper()
	all, err := runactions.New(runactions.Deps{FactoryID: "acme-app", Records: w.records, Runs: w.runs,
		Approvals: w.inbox, QAHolds: w.holds, ReleaseHolds: w.releases, Clock: intaketest.NewClock(t0)})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range all {
		if a.Spec.Name == name {
			return a
		}
	}
	t.Fatalf("no action %s", name)
	return core.Action{}
}

func call[T any](t *testing.T, a core.Action, in string) (T, error) {
	t.Helper()
	return callAs[T](t, a, in, core.Actor{})
}

// callAs is call by actor.
func callAs[T any](t *testing.T, a core.Action, in string, actor core.Actor) (T, error) {
	t.Helper()
	var out T
	res, err := a.Handle(context.Background(), core.ActionRequest{Actor: actor, Input: json.RawMessage(in)})
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("output %s: %v", res.Output, err)
	}
	return out, nil
}
