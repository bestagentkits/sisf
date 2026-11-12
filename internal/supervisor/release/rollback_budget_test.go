package release

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// withCappedBudget gives the world a capped budget of cap micros keeping
// reserve for rollbacks, with spent micros already used this period, and
// wires the budget gate into the pipeline.
func (w *world) withCappedBudget(capMicros, reserve, spent core.Micros) {
	w.t.Helper()
	confirmed := dispatcher.ConfirmationFunc(func(context.Context, core.BudgetLedger) (dispatcher.BudgetConfirmation, error) {
		return dispatcher.BudgetConfirmation{Mode: core.BudgetCapped, ConfirmedAt: startTime.Add(-time.Hour)}, nil
	})
	settings, err := json.Marshal(budget.Settings{Mode: core.BudgetCapped, CapMicros: capMicros, RollbackReserveMicros: reserve})
	if err != nil {
		w.t.Fatal(err)
	}
	err = w.store.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		if _, err := tx.PutKV(ctx, state.KVEntry{Namespace: budget.SettingsNamespace, Key: budget.SettingsKey, Value: settings}); err != nil {
			return err
		}
		return tx.AppendUsage(ctx, core.Usage{
			FactoryID: testFactory, IssueID: issueRef(1), RunID: "run-earlier", Cost: spent, Verified: true, At: startTime,
		})
	})
	if err != nil {
		w.t.Fatalf("seed the budget: %v", err)
	}
	w.cfg.Budget = budget.NewGate(budget.Config{Confirmations: confirmed, Clock: w.clock})
}

// budgetTotals reads what the ledger holds for the period.
func (w *world) budgetTotals() core.BudgetTotals {
	w.t.Helper()
	var totals core.BudgetTotals
	err := w.store.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		totals, err = tx.BudgetTotals(ctx, time.Time{})
		return err
	})
	if err != nil {
		w.t.Fatalf("budget totals: %v", err)
	}
	return totals
}

func TestAnExhaustedBudgetNeverBlocksARollbackAndItsSpendIsRecorded(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.withCappedBudget(10, 4, 10)
	w.backend.errorRate[core.ReleaseDigest(firstBuild)] = 0.10
	issue, run := issueRef(20), core.RunID("run-20")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	requireRestored(t, w, issue, run, out, err)
	totals := w.budgetTotals()
	if totals.Reserved != 0 || totals.SpentVerified != 10 || totals.SpentUnverified != DefaultRollbackCost {
		t.Fatalf("budget totals %+v, want the earlier spend plus the rollback's nominal cost and nothing held", totals)
	}
}

func TestARollbackSpendsTheRollbackReserveARunCannotReach(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.withCappedBudget(10, 4, 7)
	ctx := context.Background()
	err := w.store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		_, err := w.cfg.Budget.(*budget.Gate).Reserve(ctx, tx, core.ReservationRequest{
			FactoryID: testFactory, IssueID: issueRef(21), RunID: "run-other", Estimate: 1, IdempotencyKey: "claim/run-other",
		})
		return err
	})
	if !core.HasCode(err, core.CodeBudgetExceeded) {
		t.Fatalf("an ordinary run reserved %v into the rollback reserve, want BUDGET_EXCEEDED", err)
	}
	w.backend.errorRate[core.ReleaseDigest(firstBuild)] = 0.10
	issue, run := issueRef(21), core.RunID("run-21")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(ctx, w.request(issue, run, firstBuild))
	requireRestored(t, w, issue, run, out, err)
	totals := w.budgetTotals()
	if totals.Reserved != 0 || totals.SpentUnverified != DefaultRollbackCost {
		t.Fatalf("budget totals %+v, want the rollback's reservation settled at its nominal cost", totals)
	}
}

func TestANegativeRollbackCostIsRefused(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.cfg.RollbackCost = -1
	if _, err := New(w.cfg); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("New with a negative rollback cost: %v, want INVALID_ARGUMENT", err)
	}
}

// stalledBudget is a budget gate that never answers until its caller gives
// up, like a state store stuck behind a lock.
type stalledBudget struct{ RollbackBudget }

func (stalledBudget) ReserveRollback(ctx context.Context, _ core.BudgetLedger, _ core.ReservationRequest) (core.Reservation, error) {
	<-ctx.Done()
	return core.Reservation{}, ctx.Err()
}

func (stalledBudget) Settle(ctx context.Context, _ core.BudgetLedger, _ string, _ core.Usage) error {
	<-ctx.Done()
	return ctx.Err()
}

// finishesSoon runs fn and fails the test if it has not returned within a
// few seconds, which a rollback waiting on an unbounded budget call never does.
func finishesSoon(t *testing.T, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the rollback waited on the budget")
	}
}

func TestAnUnavailableBudgetNeverHoldsUpARollback(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.cfg.Budget, w.cfg.RollbackBudgetWait = stalledBudget{}, 20*time.Millisecond
	w.backend.errorRate[core.ReleaseDigest(firstBuild)] = 0.10
	issue, run := issueRef(22), core.RunID("run-22")
	w.seedReleaseCandidate(issue, run, firstBuild)
	var out Outcome
	var err error
	finishesSoon(t, func() { out, err = w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild)) })
	requireRestored(t, w, issue, run, out, err)
}

func TestAnOwnerRollbackIsRecordedInTheLedgerAndNeverBlockedByTheBudget(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.withCappedBudget(10, 4, 10)
	desk := newApprovalDesk(t, w)
	brokenRelease(w)
	ref := desk.file(RollbackAction, RollbackSubject(testFactory, testUnit, lkgDigest), approvals.DecisionApprove)
	if _, err := w.pipeline().OwnerRollback(context.Background(), rollbackRequest(ref)); err != nil {
		t.Fatalf("an Owner rollback with the budget exhausted: %v", err)
	}
	if totals := w.budgetTotals(); totals.Reserved != 0 || totals.SpentVerified != 10 || totals.SpentUnverified != DefaultRollbackCost {
		t.Fatalf("budget totals %+v, want the earlier spend plus the rollback's nominal cost", totals)
	}

	stalled := newWorld(t)
	stalled.cfg.Budget, stalled.cfg.RollbackBudgetWait = stalledBudget{}, 20*time.Millisecond
	desk = newApprovalDesk(t, stalled)
	brokenRelease(stalled)
	ref = desk.file(RollbackAction, RollbackSubject(testFactory, testUnit, lkgDigest), approvals.DecisionApprove)
	var err error
	finishesSoon(t, func() { _, err = stalled.pipeline().OwnerRollback(context.Background(), rollbackRequest(ref)) })
	if err != nil {
		t.Fatalf("an Owner rollback with the budget unavailable: %v", err)
	}
}
