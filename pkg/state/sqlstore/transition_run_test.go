package sqlstore_test

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

func TestAttemptOfARunIsNeverLowered(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		rec := along(t, s, developing(t, s, issue(7)), core.EventCandidateSubmitted)
		// The evaluation fails and the run goes into its second attempt.
		rec = advance(t, s, rec, core.EventQAFailed, nil)
		if rec.Attempt != 2 || rec.RunID != "run-1" {
			t.Fatalf("record after the retry = %+v", rec)
		}

		// A later step of the same run may not wind the count back.
		for name, change := range map[string]func(*core.Transition){
			"count wound back": func(tr *core.Transition) { tr.Attempt = 1 },
			"count dropped":    func(tr *core.Transition) { tr.Attempt = 0 },
		} {
			lowered := next(t, rec, core.EventCandidateSubmitted, change)
			lowered.IdempotencyKey = "resubmitted: " + name
			refused(t, s, name, lowered, core.CodeInvalidArgument)
		}
		// Keeping the count is fine.
		rec = advance(t, s, rec, core.EventCandidateSubmitted, func(tr *core.Transition) { tr.IdempotencyKey = "resubmitted" })
		if rec.Attempt != 2 {
			t.Fatalf("attempt after a step that keeps it = %d", rec.Attempt)
		}

		// Nor may a step that leaves the run unnamed, once no run holds the
		// issue any more.
		rec = along(t, s, rec, core.EventQAPassed, core.EventRolloutStarted, core.EventErrorThresholdExceeded)
		for name, change := range map[string]func(*core.Transition){
			"run left as it is": func(tr *core.Transition) { tr.RunID, tr.Attempt = "", 1 },
			"run named again":   func(tr *core.Transition) { tr.Attempt = 1 },
		} {
			refused(t, s, name, next(t, rec, core.EventRecoveryReported, change), core.CodeInvalidArgument)
		}
	})
}

func TestNewRunStartsItsOwnAttemptCount(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		// The first run needs a second attempt before its candidate passes.
		rec := along(t, s, developing(t, s, issue(7)), core.EventCandidateSubmitted, core.EventQAFailed)
		rec = advance(t, s, rec, core.EventCandidateSubmitted, func(tr *core.Transition) { tr.IdempotencyKey = "resubmitted" })
		if rec.Attempt != 2 {
			t.Fatalf("attempt of the first run = %d, want 2", rec.Attempt)
		}
		// The issue is sent back and claimed again by another run.
		rec = along(t, s, rec, core.EventQAPassed, core.EventRolloutStarted,
			core.EventErrorThresholdExceeded, core.EventRecoveryReported, core.EventRetryAuthorized)
		if rec.Attempt != 2 {
			t.Fatalf("attempt kept until the next claim = %d, want 2", rec.Attempt)
		}
		rec = claim(t, s, rec, "worker-1", "run-2")
		if rec.RunID != "run-2" || rec.Attempt != 1 {
			t.Fatalf("record of the new run = %+v", rec)
		}
	})
}

func TestCandidateAndReleaseOfAStepAreKeptWithIt(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		rec := along(t, s, developing(t, s, issue(7)), core.EventCandidateSubmitted)
		passed := next(t, rec, core.EventQAPassed, nil)
		candidate := mustTransition(t, s, passed)
		along(t, s, candidate, core.EventRolloutStarted, core.EventRolloutHealthy, core.EventPostDeployVerified)

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
		// first write, although the record has moved on and names a release
		// by now; replayed for another candidate it is not a replay.
		replayed, err := transition(s, passed)
		if err != nil || replayed != candidate {
			t.Fatalf("replay for the same candidate = %+v, %v", replayed, err)
		}
		swapped := passed
		swapped.CandidateDigest = otherCandidate
		_, err = transition(s, swapped)
		wantCode(t, err, core.CodeConflict)
		// Nor is the same key presented by another run.
		foreign := passed
		foreign.RunID = "run-2"
		_, err = transition(s, foreign)
		wantCode(t, err, core.CodeConflict)
		// A digest that is not a full content hash is refused outright.
		malformed := passed
		malformed.IdempotencyKey, malformed.CandidateDigest = "qa-passed-again", "latest"
		_, err = transition(s, malformed)
		wantCode(t, err, core.CodeInvalidArgument)
	})
}
