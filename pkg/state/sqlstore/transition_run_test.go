package sqlstore_test

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

const (
	candidateDigest core.Digest        = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	otherCandidate  core.Digest        = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	releaseDigest   core.ReleaseDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

// next builds the write that moves a record along the edge of event. The
// key is the event name, so a test takes each edge once per issue.
func next(t *testing.T, from core.StateRecord, event core.Event, change func(*core.Transition)) core.Transition {
	t.Helper()
	to, err := statemachine.Next(from.State, event)
	if err != nil {
		t.Fatalf("no edge from %s on %s: %v", from.State, event, err)
	}
	tr := step(from.IssueID, from.State, event, to, from.Version, string(event))
	tr.Attempt = from.Attempt
	if change != nil {
		change(&tr)
	}
	return tr
}

// advance takes one edge and returns the record after it.
func advance(t *testing.T, s *sqlstore.Store, from core.StateRecord, event core.Event, change func(*core.Transition)) core.StateRecord {
	t.Helper()
	return mustTransition(t, s, next(t, from, event, change))
}

func TestAttemptOfARunIsNeverLowered(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		rec := readyRecord(t, s, issue(7))
		rec = advance(t, s, rec, core.EventClaimed, func(tr *core.Transition) { tr.RunID, tr.Attempt = "run-1", 1 })
		rec = advance(t, s, rec, core.EventDevelopmentStarted, nil)
		rec = advance(t, s, rec, core.EventCandidateSubmitted, nil)
		// The evaluation fails and the run goes into its second attempt.
		rec = advance(t, s, rec, core.EventQAFailed, func(tr *core.Transition) { tr.Attempt = 2 })
		if rec.Attempt != 2 || rec.RunID != "run-1" {
			t.Fatalf("record after the retry = %+v", rec)
		}

		// A later step of the same run may not wind the count back, whether
		// it names the run or leaves it as it is.
		for name, change := range map[string]func(*core.Transition){
			"run left as it is": func(tr *core.Transition) { tr.Attempt = 1 },
			"run named again":   func(tr *core.Transition) { tr.RunID, tr.Attempt = "run-1", 1 },
			"count dropped":     func(tr *core.Transition) { tr.Attempt = 0 },
		} {
			lowered := next(t, rec, core.EventCandidateSubmitted, change)
			lowered.IdempotencyKey = "resubmitted: " + name
			_, err := transition(s, lowered)
			if !core.HasCode(err, core.CodeInvalidArgument) {
				t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, err)
			}
		}
		if stored := record(t, s, issue(7)); stored != rec {
			t.Fatalf("a refused step changed the record: %+v", stored)
		}
		// Keeping the count is fine.
		rec = advance(t, s, rec, core.EventCandidateSubmitted, func(tr *core.Transition) { tr.IdempotencyKey = "resubmitted" })
		if rec.Attempt != 2 {
			t.Fatalf("attempt after a step that keeps it = %d", rec.Attempt)
		}
	})
}

func TestNewRunStartsItsOwnAttemptCount(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		rec := readyRecord(t, s, issue(7))
		rec = advance(t, s, rec, core.EventClaimed, func(tr *core.Transition) { tr.RunID, tr.Attempt = "run-1", 3 })
		// The issue is sent back and claimed again by another run.
		for _, event := range []core.Event{
			core.EventDevelopmentStarted, core.EventCandidateSubmitted, core.EventQAPassed, core.EventRolloutStarted,
			core.EventErrorThresholdExceeded, core.EventRecoveryReported, core.EventRetryAuthorized,
		} {
			rec = advance(t, s, rec, event, nil)
		}
		again := next(t, rec, core.EventClaimed, func(tr *core.Transition) { tr.RunID, tr.Attempt = "run-2", 1 })
		again.IdempotencyKey = "claimed-again"
		rec = mustTransition(t, s, again)
		if rec.RunID != "run-2" || rec.Attempt != 1 {
			t.Fatalf("record of the new run = %+v", rec)
		}
	})
}

func TestCandidateAndReleaseOfAStepAreKeptWithIt(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		rec := readyRecord(t, s, issue(7))
		rec = advance(t, s, rec, core.EventClaimed, func(tr *core.Transition) { tr.RunID, tr.Attempt = "run-1", 1 })
		rec = advance(t, s, rec, core.EventDevelopmentStarted, nil)
		rec = advance(t, s, rec, core.EventCandidateSubmitted, nil)
		passed := next(t, rec, core.EventQAPassed, func(tr *core.Transition) { tr.CandidateDigest = candidateDigest })
		candidate := mustTransition(t, s, passed)
		rec = advance(t, s, candidate, core.EventRolloutStarted, nil)
		rec = advance(t, s, rec, core.EventRolloutHealthy, nil)
		advance(t, s, rec, core.EventPostDeployVerified, func(tr *core.Transition) { tr.ReleaseDigest = releaseDigest })

		stored := func(key string) (candidate, release string) {
			t.Helper()
			err := db.Raw(t).QueryRowContext(ctx, db.Dialect.Rebind(
				`SELECT candidate_digest, release_digest FROM transitions WHERE idempotency_key = ?`), key).
				Scan(&candidate, &release)
			if err != nil {
				t.Fatalf("read the stored step %s: %v", key, err)
			}
			return candidate, release
		}
		if c, r := stored(string(core.EventQAPassed)); c != string(candidateDigest) || r != "" {
			t.Fatalf("stored step into ReleaseCandidate = %q, %q", c, r)
		}
		if c, r := stored(string(core.EventPostDeployVerified)); c != "" || r != string(releaseDigest) {
			t.Fatalf("stored step into Verified = %q, %q", c, r)
		}

		// The step replayed for the same candidate is answered from the
		// first write; replayed for another candidate it is not a replay.
		replayed, err := transition(s, passed)
		if err != nil || replayed != candidate {
			t.Fatalf("replay for the same candidate = %+v, %v", replayed, err)
		}
		swapped := passed
		swapped.CandidateDigest = otherCandidate
		_, err = transition(s, swapped)
		wantCode(t, err, core.CodeConflict)
		// A digest that is not a full content hash is refused outright.
		malformed := passed
		malformed.IdempotencyKey, malformed.CandidateDigest = "qa-passed-again", "latest"
		_, err = transition(s, malformed)
		wantCode(t, err, core.CodeInvalidArgument)
	})
}
