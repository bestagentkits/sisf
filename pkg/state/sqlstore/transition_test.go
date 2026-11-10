package sqlstore_test

import (
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

func TestTransitionCreatesTheRecordAndAdvancesItsVersion(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		created := mustTransition(t, s, received(issue(7)))
		if created.State != core.StateIntake || created.Version != 1 || created.FactoryID != sqlstoretest.Factory {
			t.Fatalf("created record = %+v", created)
		}
		if err := created.Validate(); err != nil {
			t.Fatalf("the created record must be well formed: %v", err)
		}
		db.Clock.Advance(time.Minute)
		moved := mustTransition(t, s, step(issue(7), core.StateIntake, core.EventInfoSufficient, core.StateScopeDraft, 1, "scoped"))
		if moved.State != core.StateScopeDraft || moved.Version != 2 || moved.IdempotencyKey != "scoped" {
			t.Fatalf("moved record = %+v", moved)
		}
		if !moved.UpdatedAt.Equal(db.Clock.Now()) {
			t.Fatalf("UpdatedAt = %s, want the instant of the write %s", moved.UpdatedAt, db.Clock.Now())
		}
		if stored := record(t, s, issue(7)); stored != moved {
			t.Fatalf("stored record %+v differs from the returned one %+v", stored, moved)
		}
	})
}

func TestTransitionOnAStaleVersionIsRefusedAndWritesNothing(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		mustTransition(t, s, received(issue(7)))
		current := mustTransition(t, s, step(issue(7), core.StateIntake, core.EventInfoMissing, core.StateClarifying, 1, "asked"))

		// A writer that still holds version 1 lost the race.
		_, err := transition(s, step(issue(7), core.StateIntake, core.EventInfoSufficient, core.StateScopeDraft, 1, "late"))
		wantCode(t, err, core.CodeConflict)
		// The right state with the wrong version is refused as well.
		_, err = transition(s, step(issue(7), core.StateClarifying, core.EventClarificationCompleted, core.StateScopeDraft, 1, "late-2"))
		wantCode(t, err, core.CodeConflict)
		// So is the right version with a state the record is not in.
		_, err = transition(s, step(issue(7), core.StateIntake, core.EventInfoSufficient, core.StateScopeDraft, 2, "late-3"))
		wantCode(t, err, core.CodeConflict)
		// A record that does not exist cannot be advanced.
		_, err = transition(s, step(issue(8), core.StateIntake, core.EventInfoSufficient, core.StateScopeDraft, 1, "ghost"))
		wantCode(t, err, core.CodeConflict)

		if stored := record(t, s, issue(7)); stored != current {
			t.Fatalf("a refused write changed the record: %+v", stored)
		}
	})
}

func TestCreatingARecordThatExistsIsRefused(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		first := mustTransition(t, s, received(issue(7)))
		again := received(issue(7))
		again.IdempotencyKey = "received-again"
		_, err := transition(s, again)
		wantCode(t, err, core.CodeConflict)
		if stored := record(t, s, issue(7)); stored != first {
			t.Fatalf("a refused creation changed the record: %+v", stored)
		}
	})
}

func TestTransitionOutsideTheLifecycleIsRefused(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		created := mustTransition(t, s, received(issue(7)))

		// Intake has no step straight to Ready.
		_, err := transition(s, step(issue(7), core.StateIntake, core.EventScopePermitted, core.StateReady, 1, "shortcut"))
		wantCode(t, err, core.CodeInvalidTransition)
		// Nor straight to Closed, although the request matches the stored
		// version and state: the store checks the table on every write.
		_, err = transition(s, step(issue(7), core.StateIntake, core.EventIssueClosed, core.StateClosed, 1, "close"))
		wantCode(t, err, core.CodeInvalidTransition)
		// A real event may not be pointed at a state it does not lead to.
		_, err = transition(s, step(issue(7), core.StateIntake, core.EventInfoSufficient, core.StateClosed, 1, "redirect"))
		wantCode(t, err, core.CodeInvalidTransition)
		// A malformed request is refused before anything is read.
		unkeyed := step(issue(7), core.StateIntake, core.EventInfoSufficient, core.StateScopeDraft, 1, "")
		_, err = transition(s, unkeyed)
		wantCode(t, err, core.CodeInvalidArgument)

		if stored := record(t, s, issue(7)); stored != created {
			t.Fatalf("a refused write changed the record: %+v", stored)
		}
	})
}

func TestRepeatedIdempotencyKeyReturnsTheFirstWriteWithoutWritingAgain(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		mustTransition(t, s, received(issue(7)))
		scoped := step(issue(7), core.StateIntake, core.EventInfoSufficient, core.StateScopeDraft, 1, "scoped")
		first := mustTransition(t, s, scoped)
		latest := mustTransition(t, s, step(issue(7), core.StateScopeDraft, core.EventScopePermitted, core.StateReady, 2, "permitted"))

		// The same write delivered again, after the record has moved on.
		db.Clock.Advance(time.Hour)
		replayed, err := transition(s, scoped)
		if err != nil {
			t.Fatalf("a replayed write must succeed: %v", err)
		}
		if replayed != first {
			t.Fatalf("replay returned %+v, want the first write %+v", replayed, first)
		}
		if stored := record(t, s, issue(7)); stored != latest {
			t.Fatalf("a replayed write changed the record: %+v", stored)
		}

		// The key names one step. Using it for another is not a replay.
		other := step(issue(7), core.StateReady, core.EventClaimed, core.StateClaimed, 3, "scoped")
		_, err = transition(s, other)
		wantCode(t, err, core.CodeConflict)
	})
}

func TestTransitionUnderAStaleFencingTokenIsRefused(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneAgent)
		ready := readyRecord(t, db.Open(t, core.ZoneControl), issue(7))
		resource := state.IssueLeaseResource(issue(7))

		var crashed, successor state.Lease
		mustUpdate(t, s, func(tx state.Tx) (err error) {
			crashed, err = tx.AcquireLease(ctx, resource, "dispatcher-1", time.Minute)
			return err
		})
		db.Clock.Advance(2 * time.Minute)
		mustUpdate(t, s, func(tx state.Tx) (err error) {
			successor, err = tx.AcquireLease(ctx, resource, "dispatcher-2", time.Minute)
			return err
		})

		// The holder that lost its lease reports late.
		stale := step(issue(7), core.StateReady, core.EventClaimed, core.StateClaimed, ready.Version, "claim/1")
		stale.LeaseOwner, stale.FencingToken, stale.RunID = crashed.Owner, crashed.Token, "run-1"
		_, err := transition(s, stale)
		wantCode(t, err, core.CodeStaleFencingToken)
		if stored := record(t, s, issue(7)); stored != ready {
			t.Fatalf("a fenced-off write changed the record: %+v", stored)
		}

		claim := step(issue(7), core.StateReady, core.EventClaimed, core.StateClaimed, ready.Version, "claim/2")
		claim.LeaseOwner, claim.FencingToken, claim.RunID, claim.Attempt = successor.Owner, successor.Token, "run-2", 1
		claimed := mustTransition(t, s, claim)
		if claimed.LeaseOwner != "dispatcher-2" || claimed.FencingToken != successor.Token || claimed.RunID != "run-2" || claimed.Attempt != 1 {
			t.Fatalf("claimed record = %+v", claimed)
		}

		// The run goes on under the lease it claimed with, and under no other.
		start := step(issue(7), core.StateClaimed, core.EventDevelopmentStarted, core.StateDeveloping, claimed.Version, "develop/run-2")
		start.Attempt, start.RunID = claimed.Attempt, claimed.RunID
		_, err = transition(s, start)
		wantCode(t, err, core.CodeStaleFencingToken)
		start.LeaseOwner, start.FencingToken = crashed.Owner, crashed.Token
		_, err = transition(s, start)
		wantCode(t, err, core.CodeStaleFencingToken)
		start.LeaseOwner, start.FencingToken = successor.Owner, successor.Token
		started := mustTransition(t, s, start)
		if started.LeaseOwner != "dispatcher-2" || started.FencingToken != successor.Token || started.RunID != "run-2" {
			t.Fatalf("a step of the run dropped the lease or run: %+v", started)
		}
	})
}

func TestConcurrentWritersOnOneVersionLetExactlyOneWin(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		// Two stores stand for two processes sharing the database.
		control, agent := db.Open(t, core.ZoneControl), db.Open(t, core.ZoneAgent)
		ready := readyRecord(t, control, issue(7))

		// Every writer holds the current lease, as copies of one worker would,
		// so only the version decides between them.
		lease := leaseOn(t, agent, issue(7), "worker-1")
		const writers = 8
		var wg sync.WaitGroup
		errs := make([]error, writers)
		for i := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s := control
				if i%2 == 1 {
					s = agent
				}
				// Each writer has a key of its own, so none replays another.
				tr := step(issue(7), core.StateReady, core.EventClaimed, core.StateClaimed, ready.Version, "claim/"+string(rune('a'+i)))
				tr.LeaseOwner, tr.FencingToken, tr.RunID, tr.Attempt = lease.Owner, lease.Token, core.RunID("run-"+string(rune('a'+i))), 1
				_, errs[i] = transition(s, tr)
			}()
		}
		wg.Wait()

		won := 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case !core.HasCode(err, core.CodeConflict):
				t.Fatalf("a losing writer must see CONFLICT, got %v", err)
			}
		}
		if won != 1 {
			t.Fatalf("%d writers won the same version, want exactly 1", won)
		}
		if stored := record(t, control, issue(7)); stored.Version != ready.Version+1 || stored.State != core.StateClaimed {
			t.Fatalf("record after the race = %+v", stored)
		}
	})
}
