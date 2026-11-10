package sqlstore_test

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

func TestAWorkerThatLostItsLeaseCannotWriteWithItsOldTokenOrWithNone(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneAgent)
		rec := developing(t, db.Open(t, core.ZoneControl), issue(7))
		lost := state.Lease{Owner: rec.LeaseOwner, Token: rec.FencingToken}
		// The lease of worker-1 lapses and worker-2 takes the issue over.
		db.Clock.Advance(2 * leaseTTL)
		current := leaseOn(t, s, issue(7), "worker-2")

		hostile := map[string]struct {
			lease func(*core.Transition)
			want  core.ErrorCode
		}{
			"its old token":           {under(lost), core.CodeStaleFencingToken},
			"no token at all":         {under(state.Lease{}), core.CodeStaleFencingToken},
			"its name and token zero": {under(state.Lease{Owner: lost.Owner}), core.CodeStaleFencingToken},
			"a token not yet issued":  {under(state.Lease{Owner: lost.Owner, Token: current.Token + 1}), core.CodeStaleFencingToken},
			"the name of the holder and its old token": {
				under(state.Lease{Owner: current.Owner, Token: lost.Token}), core.CodeStaleFencingToken},
			// A token that names no owner is not a lease at all.
			"the current token without a name": {under(state.Lease{Token: current.Token}), core.CodeInvalidArgument},
		}
		for name, write := range hostile {
			late := next(t, rec, core.EventCandidateSubmitted, write.lease)
			late.IdempotencyKey = "late: " + name
			refused(t, s, "a write with "+name, late, write.want)
		}

		// The holder of the lease carries the run on.
		submitted := advance(t, s, rec, core.EventCandidateSubmitted, under(current))
		if submitted.State != core.StateQAEvaluation || submitted.LeaseOwner != "worker-2" || submitted.FencingToken != current.Token {
			t.Fatalf("the current holder could not submit its candidate: %+v", submitted)
		}
	})
}

func TestEveryStepOfARunIsRefusedWithoutALease(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		// unleased tries every step out of the state of rec without a lease,
		// the rollback included: only the holder of the lease reports on a run.
		unleased := func(rec core.StateRecord) {
			t.Helper()
			for _, edge := range statemachine.Edges() {
				if edge.From != rec.State {
					continue
				}
				write := next(t, rec, edge.Event, under(state.Lease{}))
				if edge.Event == core.EventClaimed {
					write.RunID = "run-9"
				}
				write.IdempotencyKey = "unleased: " + string(edge.Event)
				refused(t, s, string(rec.State)+" on "+string(edge.Event), write, core.CodeStaleFencingToken)
			}
		}

		rec := readyRecord(t, s, issue(7))
		unleased(rec)
		rec = claim(t, s, rec, "worker-1", "run-1")
		for _, event := range []core.Event{
			core.EventDevelopmentStarted, core.EventCandidateSubmitted, core.EventQAPassed,
			core.EventRolloutStarted, core.EventRolloutHealthy,
		} {
			unleased(rec)
			rec = advance(t, s, rec, event, nil)
		}
		unleased(rec)
		if rec.State != core.StateMonitoring {
			t.Fatalf("the run ended in %s, want %s", rec.State, core.StateMonitoring)
		}

		// Once the release has left Monitoring no run holds the issue, and a
		// step without a lease is an ordinary versioned write again.
		rec = advance(t, s, rec, core.EventRegressionDetected, nil)
		reported := advance(t, s, rec, core.EventRecoveryReported, nil)
		if reported.State != core.StateBlocked || reported.LeaseOwner != "" || reported.FencingToken != 0 {
			t.Fatalf("the record after an unleased step = %+v", reported)
		}
	})
}

func TestAStepKeepsTheLeaseItWasWrittenUnderOnTheRecord(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		rec := developing(t, s, issue(7))
		if rec.LeaseOwner != "worker-1" || rec.FencingToken != 1 || rec.RunID != "run-1" {
			t.Fatalf("the record of a held issue = %+v", rec)
		}
		// The same worker restarts and takes its lease again, under a new token.
		again := leaseOn(t, s, issue(7), "worker-1")
		refused(t, s, "a write of the earlier incarnation", next(t, rec, core.EventCandidateSubmitted, nil), core.CodeStaleFencingToken)
		submitted := advance(t, s, rec, core.EventCandidateSubmitted, under(again))
		if submitted.FencingToken != again.Token || submitted.RunID != "run-1" {
			t.Fatalf("the record after the restart = %+v", submitted)
		}
		if stored := record(t, s, issue(7)); stored != submitted {
			t.Fatalf("stored record %+v differs from the returned one %+v", stored, submitted)
		}
	})
}
