package stages

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// A close that can never succeed, here because the dispatcher's record of
// the run is missing, is not tried for ever: after a bounded number of
// passes the run is recorded once as abandoned, with the code and the
// reason, its close-due entry is gone, and later passes report nothing
// about it. An entry in the format written before failures were counted,
// the run's ID alone, is read and counted the same way. A close that fails
// for a passing reason is not counted.
func TestACloseThatCanNeverSucceedIsAbandonedOnceWithItsReason(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		control := db.Open(t, core.ZoneControl)
		gate := budget.NewGate(budget.Config{Clock: db.Clock,
			Confirmations: dispatcher.ConfirmationFunc(func(context.Context, core.BudgetLedger) (dispatcher.BudgetConfirmation, error) {
				return dispatcher.BudgetConfirmation{Mode: core.BudgetUnlimited, AlertOff: true, ConfirmedAt: db.Clock.Now().Add(-time.Minute)}, nil
			})})
		s := &QAStage{FactoryID: sqlstoretest.Factory, Owner: "supervisor-qa", State: control, Budget: gate, Clock: db.Clock}
		current, legacy := core.RunID("run-21"), core.RunID("run-22")
		legacyValue, _ := json.Marshal(string(legacy))
		if err := control.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			if err := putCloseDue(ctx, tx, current); err != nil {
				return err
			}
			_, err := tx.PutKV(ctx, state.KVEntry{Namespace: Namespace, Key: closeDuePrefix + string(legacy), Value: legacyValue})
			return err
		}); err != nil {
			t.Fatal(err)
		}

		for pass := 1; pass < closeDueBound; pass++ {
			res := s.Pass(ctx)
			if len(res.Errors) != 2 || !strings.Contains(res.Errors[0].Error(), "tries") || strings.Contains(res.Errors[0].Error(), "no longer tried") {
				t.Fatalf("pass %d: %v", pass, res.Errors)
			}
		}
		res := s.Pass(ctx)
		if len(res.Errors) != 2 {
			t.Fatalf("the pass that gives up: %v", res.Errors)
		}
		for _, err := range res.Errors {
			if !strings.Contains(err.Error(), "no longer tried") || !core.HasCode(err, core.CodeNotFound) {
				t.Errorf("the run is not reported as abandoned with its code: %v", err)
			}
		}
		for _, run := range []core.RunID{current, legacy} {
			var abandoned CloseAbandoned
			err := control.View(ctx, func(ctx context.Context, tx state.Tx) error {
				var err error
				abandoned, err = ReadCloseAbandoned(ctx, tx, run)
				if err != nil {
					return err
				}
				_, err = tx.GetKV(ctx, Namespace, closeDuePrefix+string(run))
				if !core.HasCode(err, core.CodeNotFound) {
					t.Errorf("%s: the close-due entry is still there: %v", run, err)
				}
				return nil
			})
			if err != nil || abandoned.RunID != run || abandoned.Code != core.CodeNotFound || abandoned.Failures != closeDueBound ||
				abandoned.Reason == "" || !abandoned.AbandonedAt.Equal(db.Clock.Now().UTC()) {
				t.Errorf("%s: the abandoned record: %+v, %v", run, abandoned, err)
			}
		}
		for range 2 {
			if res := s.Pass(ctx); len(res.Errors) != 0 {
				t.Fatalf("an abandoned close was reported again: %v", res.Errors)
			}
		}
	})
}

// A close that fails for a passing reason, here a busy store, is tried on
// every pass and never counted towards abandoning it, and it closes once
// the store answers again.
func TestACloseThatFailsForAPassingReasonIsNeverAbandoned(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		agentZone, control := db.Open(t, core.ZoneAgent), db.Open(t, core.ZoneControl)
		gate := budget.NewGate(budget.Config{Clock: db.Clock,
			Confirmations: dispatcher.ConfirmationFunc(func(context.Context, core.BudgetLedger) (dispatcher.BudgetConfirmation, error) {
				return dispatcher.BudgetConfirmation{Mode: core.BudgetUnlimited, AlertOff: true, ConfirmedAt: db.Clock.Now().Add(-time.Minute)}, nil
			})})
		issue, run := core.IssueRef{Owner: "acme", Repo: "app", Number: 23}, core.RunID("run-23")
		if err := agentZone.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			reservation, err := gate.Reserve(ctx, tx, core.ReservationRequest{FactoryID: sqlstoretest.Factory, IssueID: issue, RunID: run,
				IdempotencyKey: dispatcher.ClaimIdempotencyKey(run)})
			if err != nil {
				return err
			}
			raw, _ := json.Marshal(dispatcher.RunRecord{Issue: issue, RunID: run, ReservationID: reservation.ID, Status: dispatcher.RunSucceeded})
			_, err = tx.PutKV(ctx, state.KVEntry{Namespace: handoff.Namespace, Key: "run/" + string(run), Value: raw})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		busy := 2 * closeDueBound
		closer := &flakyCloser{ReservationCloser: gate}
		closer.failures.Store(int32(busy))
		s := &QAStage{FactoryID: sqlstoretest.Factory, Owner: "supervisor-qa", State: control, Budget: closer, Clock: db.Clock}
		if err := control.Update(ctx, func(ctx context.Context, tx state.Tx) error { return putCloseDue(ctx, tx, run) }); err != nil {
			t.Fatal(err)
		}
		for pass := range busy {
			if res := s.Pass(ctx); len(res.Errors) != 1 || !core.HasCode(res.Errors[0], core.CodeUnavailable) {
				t.Fatalf("pass %d: %v", pass, res.Errors)
			}
		}
		if res := s.Pass(ctx); len(res.Errors) != 0 {
			t.Fatalf("the close once the store answers: %v", res.Errors)
		}
		if err := control.View(ctx, func(ctx context.Context, tx state.Tx) error {
			if _, err := ReadCloseAbandoned(ctx, tx, run); !core.HasCode(err, core.CodeNotFound) {
				t.Errorf("a close that failed for a passing reason was abandoned: %v", err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
