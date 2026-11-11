package release

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

// seedVerified writes the lifecycle of issue up to Verified for run, with
// build released, as the supervisor writes it, and lets the lease go.
func (w *world) seedVerified(issue core.IssueRef, run core.RunID, build core.Digest) {
	w.t.Helper()
	w.seedInFlight(issue, run, build, core.StateMonitoring)
	w.clock.Advance(3 * DefaultWindow)
	err := w.store.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		lease, err := tx.AcquireLease(ctx, state.IssueLeaseResource(issue), DefaultLeaseOwner, DefaultWindow)
		if err != nil {
			return err
		}
		record, err := tx.GetRecord(ctx, issue)
		if err != nil {
			return err
		}
		if _, err := tx.Transition(ctx, core.Transition{
			IssueID: issue, From: record.State, To: core.StateVerified, Event: core.EventPostDeployVerified,
			ExpectedVersion: record.Version, IdempotencyKey: "release/" + string(run) + "/verified", RunID: run,
			Attempt: record.Attempt, LeaseOwner: lease.Owner, FencingToken: lease.Token, ActorID: actorID,
			ReleaseDigest: core.ReleaseDigest(build),
		}); err != nil {
			return err
		}
		return tx.ReleaseLease(ctx, lease)
	})
	if err != nil {
		w.t.Fatalf("seed %s to Verified: %v", issue, err)
	}
}

// Which unit a later verified release is a build of is read from its
// build record. What it still owes is a document the control zone writes,
// so one forged to name this unit for the release of another unit does not
// supersede this release: the release, which no longer serves while the
// LKG does, is not finished as if a later release had replaced it.
func TestAForgedFinishDueOfAnotherUnitsReleaseDoesNotSupersedeARelease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	lander := &countingLander{}
	w.cfg.Lander = lander
	unlock := w.lockTrustVolume()
	issue, run := issueRef(230), core.RunID("run-230")
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)
	if out, err := w.pipeline().Release(ctx, req); err == nil || out.State != core.StateVerified {
		t.Fatalf("Release = %+v, %v; want held in Verified on the LKG", out, err)
	}
	unlock()

	other, otherRun := issueRef(231), core.RunID("run-231")
	w.seedVerified(other, otherRun, secondBuild)
	w.seedBuild(otherRun, 1, secondBuild, "worker")
	if err := w.store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		return putFinishDue(ctx, tx, FinishDue{IssueID: other, RunID: otherRun, Release: core.ReleaseDigest(secondBuild), Unit: testUnit})
	}); err != nil {
		t.Fatal(err)
	}
	w.serve(lkgDigest)

	out, err := w.pipeline().FinishVerified(ctx, req)
	if out.Result == ResultClosed {
		t.Fatalf("FinishVerified = %+v, %v; the release was finished as superseded by another unit's release", out, err)
	}
	if err != nil || out.Result != ResultBlocked || out.Cause != statemachine.BlockReleaseUnjudged {
		t.Fatalf("FinishVerified = %+v, %v; want blocked as not judged", out, err)
	}
	if lander.lands.Load() != 0 || w.published() != 0 {
		t.Fatal("a release that no longer serves was merged or published")
	}
}
