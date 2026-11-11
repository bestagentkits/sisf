package acceptance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// leaveInCanary writes the issue's record on to Canary as a supervisor
// that started the release's rollout and then stopped would have left it:
// under the issue lease it took as the release pipeline and never gave up.
func leaveInCanary(t *testing.T, f *harness.Factory, number int) core.StateRecord {
	t.Helper()
	ctx := context.Background()
	var canary core.StateRecord
	err := f.ControlStore.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		record, err := tx.GetRecord(ctx, f.Issue(number))
		if err != nil {
			return err
		}
		lease, err := tx.AcquireLease(ctx, state.IssueLeaseResource(record.IssueID), release.DefaultLeaseOwner, time.Hour)
		if err != nil {
			return err
		}
		canary, err = tx.Transition(ctx, core.Transition{
			IssueID: record.IssueID, From: record.State, To: core.StateCanary, Event: core.EventRolloutStarted,
			ExpectedVersion: record.Version, IdempotencyKey: "release/" + string(record.RunID) + "/rollout_started",
			RunID: record.RunID, Attempt: record.Attempt, LeaseOwner: lease.Owner, FencingToken: lease.Token, ActorID: "supervisor",
			CandidateDigest: record.CandidateDigest, ReleaseDigest: core.ReleaseDigest(record.CandidateDigest),
		})
		return err
	})
	if err != nil {
		t.Fatalf("leave issue %d in Canary: %v", number, err)
	}
	return canary
}

// A release a stopped supervisor left in Canary is not left there: the
// supervisor's next pass restores the last known good release through the
// deployment, records RolledBack and then Blocked with the cause that the
// release was interrupted, writes the recovery report and drops the hold
// the release once had. The issue's progress says it is blocked and how to
// retry it, and the Owner's retry returns it to Ready.
func TestAReleaseLeftInCanaryByAStoppedSupervisorIsRestoredAndBlocked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	f.Quiet()
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	f.Cycle(4)
	held := f.Record(number)
	if held.State != core.StateReleaseCandidate {
		t.Fatalf("the release is not held in ReleaseCandidate: %+v", held)
	}
	lkg := f.Trust.Installed["app"]
	canary := leaveInCanary(t, f, number)

	f.Busy()
	f.Cycle(1)
	wantSurfaces(t, f, number, core.StateBlocked)
	blocked := f.Record(number)
	if blocked.RunID != canary.RunID || blocked.Version != canary.Version+2 {
		t.Fatalf("the release was not recorded RolledBack and then Blocked: %+v after %+v", blocked, canary)
	}
	stop := runStopOf(t, f, blocked.RunID)
	if stop.Cause != statemachine.BlockReleaseInterrupted || stop.RestoredTo != core.ReleaseDigest(lkg) {
		t.Fatalf("the record of the interrupted release: %+v, want restored to %s", stop, lkg)
	}
	var report release.RecoveryReport
	err := f.ControlStore.View(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, release.RecoveryNamespace, release.RecoveryKey(blocked.RunID))
		if err != nil {
			return err
		}
		if err := json.Unmarshal(entry.Value, &report); err != nil {
			return err
		}
		_, err = handoff.ReadReleaseHold(ctx, tx, blocked.RunID, blocked.Attempt)
		if !core.HasCode(err, core.CodeNotFound) {
			t.Errorf("the hold of the interrupted release is still recorded: %v", err)
		}
		return nil
	})
	if err != nil || report.RunID != blocked.RunID || report.RestoredTo != core.ReleaseDigest(lkg) {
		t.Fatalf("the recovery report: %+v, %v", report, err)
	}
	retry := "factoryd issue retry --issue " + f.Issue(number).String()
	if c := commentContaining(t, f, number, "<!-- sisf:progress "); !strings.Contains(c, "**Blocked:**") || !strings.Contains(c, retry) ||
		strings.Contains(c, "Release waiting") {
		t.Errorf("the progress comment does not say the release was interrupted and blocked:\n%s", c)
	}

	// Nothing recovers it twice.
	f.Cycle(1)
	if got := f.Record(number); got.Version != blocked.Version {
		t.Fatalf("a recovered release was recovered again: %+v", got)
	}
	if _, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f)); err != nil {
		t.Fatalf("the Owner's retry: %v", err)
	}
	if got := f.Record(number).State; got != core.StateReady {
		t.Fatalf("the retried issue is %s, want Ready", got)
	}
}
