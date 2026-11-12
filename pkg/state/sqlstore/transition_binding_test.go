package sqlstore_test

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

func TestAStepOfAnotherRunIsRefused(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)

		// An issue nobody claimed has no run, so no step of it may name one.
		named := received(issue(8))
		named.RunID = "run-1"
		refused(t, s, "a record created for a run", named, core.CodeConflict)
		scoped := along(t, s, mustTransition(t, s, received(issue(8))), core.EventInfoSufficient)
		refused(t, s, "a step of scoping that names a run",
			next(t, scoped, core.EventScopePermitted, func(tr *core.Transition) { tr.RunID = "run-1" }), core.CodeConflict)

		rec := developing(t, s, issue(7))
		refused(t, s, "a step that names another run",
			next(t, rec, core.EventCandidateSubmitted, func(tr *core.Transition) { tr.RunID = "run-2" }), core.CodeConflict)
		refused(t, s, "a step that names no run while one holds the issue",
			next(t, rec, core.EventCandidateSubmitted, func(tr *core.Transition) { tr.RunID = "" }), core.CodeConflict)

		// After the run ended a step may leave the run unnamed, and still may
		// not name another one.
		rec = along(t, s, rec, core.EventCandidateSubmitted, core.EventQAPassed, core.EventRolloutStarted, core.EventErrorThresholdExceeded)
		refused(t, s, "a report on a run that is not the one rolled back",
			next(t, rec, core.EventRecoveryReported, func(tr *core.Transition) { tr.RunID = "run-2" }), core.CodeConflict)
		reported := advance(t, s, rec, core.EventRecoveryReported, func(tr *core.Transition) { tr.RunID = "" })
		if reported.RunID != "run-1" {
			t.Fatalf("a step that names no run changed the run on record: %+v", reported)
		}
	})
}

func TestAClaimMustNameARunTheIssueHasNotHad(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		ready := readyRecord(t, s, issue(7))
		unnamed := claimWrite(t, s, ready, "worker-1", "")
		refused(t, s, "a first claim that names no run", unnamed, core.CodeConflict)

		// The run is rolled back, its recovery reported and a retry allowed.
		rec := along(t, s, claim(t, s, ready, "worker-1", "run-1"),
			core.EventDevelopmentStarted, core.EventCandidateSubmitted, core.EventQAPassed, core.EventRolloutStarted,
			core.EventErrorThresholdExceeded, core.EventRecoveryReported, core.EventRetryAuthorized)
		if rec.State != core.StateReady || rec.RunID != "run-1" || rec.CandidateDigest != candidateDigest || rec.ReleaseDigest != releaseDigest {
			t.Fatalf("the record back in Ready = %+v", rec)
		}
		db.Clock.Advance(2 * leaseTTL)
		reuse := claimWrite(t, s, rec, "worker-2", "run-1")
		// Under the key of the first claim it would be a replay of that claim.
		reuse.IdempotencyKey = "claimed again"
		refused(t, s, "a claim that reuses the run of the last claim", reuse, core.CodeConflict)
		again := claimWrite(t, s, rec, "worker-2", "")
		again.IdempotencyKey = "claimed: no run"
		refused(t, s, "a later claim that names no run", again, core.CodeConflict)

		// A new run inherits neither the candidate nor the release of the old one.
		claimed := claim(t, s, rec, "worker-2", "run-2")
		if claimed.RunID != "run-2" || claimed.CandidateDigest != "" || claimed.ReleaseDigest != "" || claimed.Attempt != 1 {
			t.Fatalf("the new run inherited the old run, candidate or release: %+v", claimed)
		}
		if stored := record(t, s, issue(7)); stored != claimed {
			t.Fatalf("stored record %+v differs from the returned one %+v", stored, claimed)
		}
	})
}

func TestCandidateAndReleaseAreRecordedOnlyOnTheStepsThatSetThem(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		candidate := func(d core.Digest) func(*core.Transition) {
			return func(tr *core.Transition) { tr.CandidateDigest = d }
		}
		release := func(d core.ReleaseDigest) func(*core.Transition) {
			return func(tr *core.Transition) { tr.ReleaseDigest = d }
		}
		// keeps fails the test unless the record names exactly this candidate
		// and release, as written and as stored.
		keeps := func(rec core.StateRecord, c core.Digest, r core.ReleaseDigest) {
			t.Helper()
			if rec.CandidateDigest != c || rec.ReleaseDigest != r {
				t.Fatalf("the record in %s names candidate %q and release %q, want %q and %q",
					rec.State, rec.CandidateDigest, rec.ReleaseDigest, c, r)
			}
			if stored := record(t, s, rec.IssueID); stored != rec {
				t.Fatalf("stored record %+v differs from the returned one %+v", stored, rec)
			}
		}

		rec := claim(t, s, readyRecord(t, s, issue(7)), "worker-1", "run-1")
		refused(t, s, "a candidate named before one was submitted",
			next(t, rec, core.EventDevelopmentStarted, candidate(candidateDigest)), core.CodeConflict)
		refused(t, s, "a release named before one was rolled out",
			next(t, rec, core.EventDevelopmentStarted, release(releaseDigest)), core.CodeConflict)
		rec = advance(t, s, rec, core.EventDevelopmentStarted, nil)
		keeps(rec, "", "")

		refused(t, s, "a submission that names no candidate",
			next(t, rec, core.EventCandidateSubmitted, candidate("")), core.CodeConflict)
		refused(t, s, "a submission that names a release",
			next(t, rec, core.EventCandidateSubmitted, release(releaseDigest)), core.CodeConflict)
		rec = advance(t, s, rec, core.EventCandidateSubmitted, nil)
		keeps(rec, candidateDigest, "")

		refused(t, s, "QA failing another candidate than the one submitted",
			next(t, rec, core.EventQAFailed, candidate(otherCandidate)), core.CodeConflict)
		refused(t, s, "QA passing another candidate than the one submitted",
			next(t, rec, core.EventQAPassed, candidate(otherCandidate)), core.CodeConflict)
		refused(t, s, "QA passing no candidate",
			next(t, rec, core.EventQAPassed, candidate("")), core.CodeConflict)
		refused(t, s, "QA passing a release",
			next(t, rec, core.EventQAPassed, release(releaseDigest)), core.CodeConflict)
		// The evaluation fails and the run submits another build.
		rec = advance(t, s, rec, core.EventQAFailed, nil)
		keeps(rec, candidateDigest, "")
		rec = advance(t, s, rec, core.EventCandidateSubmitted, func(tr *core.Transition) {
			tr.IdempotencyKey, tr.CandidateDigest = "resubmitted", otherCandidate
		})
		keeps(rec, otherCandidate, "")
		rec = advance(t, s, rec, core.EventQAPassed, nil)
		keeps(rec, otherCandidate, "")

		refused(t, s, "a rollout of another candidate than the one QA passed",
			next(t, rec, core.EventRolloutStarted, candidate(candidateDigest)), core.CodeConflict)
		refused(t, s, "a rollout that names no release",
			next(t, rec, core.EventRolloutStarted, release("")), core.CodeConflict)
		rec = advance(t, s, rec, core.EventRolloutStarted, nil)
		keeps(rec, otherCandidate, releaseDigest)

		refused(t, s, "another release than the one rolled out reported healthy",
			next(t, rec, core.EventRolloutHealthy, release(otherRelease)), core.CodeConflict)
		refused(t, s, "no release reported healthy",
			next(t, rec, core.EventRolloutHealthy, release("")), core.CodeConflict)
		refused(t, s, "a rollback that names another release",
			next(t, rec, core.EventErrorThresholdExceeded, release(otherRelease)), core.CodeConflict)
		rec = advance(t, s, rec, core.EventRolloutHealthy, nil)
		keeps(rec, otherCandidate, releaseDigest)

		refused(t, s, "another release than the one rolled out verified",
			next(t, rec, core.EventPostDeployVerified, release(otherRelease)), core.CodeConflict)
		// A step may repeat what the record names; it changes neither.
		rec = advance(t, s, rec, core.EventPostDeployVerified, candidate(otherCandidate))
		keeps(rec, otherCandidate, releaseDigest)
	})
}

func TestTheRecordDigestColumnsLeaveThePreviousReleaseWorking(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		// The previous release writes a record without the columns it does not
		// know. The write must still succeed, and the record names nothing.
		_, err := db.Raw(t).ExecContext(ctx, db.Dialect.Rebind(`INSERT INTO records
			(factory_id, issue, state, version, idempotency_key, lease_owner, fencing_token, attempt, run_id, updated_at)
			VALUES (?, ?, ?, 1, 'received', '', 0, 0, '', 1)`),
			string(sqlstoretest.Factory), issue(7).String(), string(core.StateIntake))
		if err != nil {
			t.Fatalf("a write of the previous release failed on the new schema: %v", err)
		}
		got := record(t, db.Open(t, core.ZoneControl), issue(7))
		if got.State != core.StateIntake || got.CandidateDigest != "" || got.ReleaseDigest != "" {
			t.Fatalf("record written by the previous release = %+v", got)
		}
	})
}
