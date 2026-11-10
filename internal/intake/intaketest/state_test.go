package intaketest_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

var issue = core.IssueRef{Owner: "acme", Repo: "app", Number: 7}

// write takes the stored record of the issue along the edge of event.
func write(s *intaketest.State, event core.Event, change func(*core.Transition)) (core.StateRecord, error) {
	var written core.StateRecord
	err := s.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		// An issue without a record yet starts from the zero record.
		from, err := tx.GetRecord(ctx, issue)
		if err != nil && !core.HasCode(err, core.CodeNotFound) {
			return err
		}
		to, err := statemachine.Next(from.State, event)
		if err != nil {
			return err
		}
		t := core.Transition{IssueID: issue, From: from.State, To: to, Event: event, ExpectedVersion: from.Version,
			IdempotencyKey: string(event), ActorID: "test", Attempt: from.Attempt, RunID: from.RunID}
		if change != nil {
			change(&t)
		}
		written, err = tx.Transition(ctx, t)
		return err
	})
	return written, err
}

// The fake follows the rules of the real store, so a test that passes on it
// did not pass by writing what the real store refuses.
func TestTheInMemoryStoreRefusesWhatTheRealStoreRefuses(t *testing.T) {
	t.Parallel()
	s := intaketest.NewState(intaketest.NewClock(time.Date(2026, 11, 10, 7, 0, 0, 0, time.UTC)))
	for _, event := range []core.Event{core.EventIssueReceived, core.EventInfoSufficient} {
		if _, err := write(s, event, nil); err != nil {
			t.Fatalf("%s: %v", event, err)
		}
	}

	// No run holds the issue yet, so a step cannot name one.
	draft, _ := s.Record(issue)
	_, err := write(s, core.EventScopePermitted, func(tr *core.Transition) { tr.RunID = "run-1" })
	if !core.HasCode(err, core.CodeConflict) {
		t.Errorf("a step of a run the issue does not have: want CONFLICT, got %v", err)
	}
	if after, _ := s.Record(issue); after != draft {
		t.Fatalf("a refused write changed the record: %+v", after)
	}

	ready, err := write(s, core.EventScopePermitted, nil)
	if err != nil || ready.State != core.StateReady {
		t.Fatalf("ready = %+v, %v", ready, err)
	}
	// The fake issues no leases, so no claim gets through it.
	claims := map[string]func(*core.Transition){
		"without a lease": func(tr *core.Transition) { tr.RunID, tr.Attempt = "run-1", 1 },
		"under a lease nobody issued": func(tr *core.Transition) {
			tr.RunID, tr.Attempt, tr.LeaseOwner, tr.FencingToken = "run-1", 1, "worker-1", 1
		},
	}
	for name, claim := range claims {
		if _, err := write(s, core.EventClaimed, claim); !core.HasCode(err, core.CodeStaleFencingToken) {
			t.Errorf("a claim %s: want STALE_FENCING_TOKEN, got %v", name, err)
		}
	}
	if after, _ := s.Record(issue); after != ready {
		t.Fatalf("a refused claim changed the record: %+v", after)
	}
}
