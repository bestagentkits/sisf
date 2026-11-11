package sqlstore_test

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// rolledBackAndRetried takes a claimed issue through a release that is
// rolled back and an authorized retry, back to Ready. Its steps are keyed
// for round, so the issue can go round more than once.
func rolledBackAndRetried(t *testing.T, s *sqlstore.Store, claimed core.StateRecord, round string) core.StateRecord {
	t.Helper()
	rec := claimed
	for _, event := range []core.Event{
		core.EventDevelopmentStarted, core.EventCandidateSubmitted, core.EventQAPassed, core.EventRolloutStarted,
		core.EventErrorThresholdExceeded, core.EventRecoveryReported, core.EventRetryAuthorized,
	} {
		rec = advance(t, s, rec, event, func(tr *core.Transition) { tr.IdempotencyKey += "/" + round })
	}
	if rec.State != core.StateReady {
		t.Fatalf("round %s ended in %s, want Ready", round, rec.State)
	}
	return rec
}

func TestAClaimNeverNamesARunTheIssueHasHadBefore(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		ref := issue(8)
		ready := rolledBackAndRetried(t, s, claim(t, s, readyRecord(t, s, ref), "worker-1", "run-1"), "first")
		ready = rolledBackAndRetried(t, s, claim(t, s, ready, "worker-1", "run-2"), "second")
		if ready.RunID != "run-2" {
			t.Fatalf("the record names %s after the second run", ready.RunID)
		}

		// The run before the last one, and the last one, are both spent: a
		// claim naming either would let receipts of that run count again.
		for _, run := range []core.RunID{"run-1", "run-2"} {
			again := claimWrite(t, s, ready, "worker-1", run)
			again.IdempotencyKey = "claim/" + string(run) + "/again"
			refused(t, s, "a claim naming "+string(run)+" again", again, core.CodeConflict)
		}
		// A run the issue never had is claimed as before.
		if rec := claim(t, s, ready, "worker-1", "run-3"); rec.RunID != "run-3" || rec.Attempt != 1 {
			t.Fatalf("the claim of a new run wrote %+v", rec)
		}
	})
}
