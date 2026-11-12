package acceptance

import (
	"context"
	"encoding/json"
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

// The Owner often puts another policy in force because an issue is blocked,
// for example to move the baseline after a release was refused for lacking
// the last known good release. The retry of an issue whose run stopped for
// any reason then has its scope judged under the policy in force and pinned
// to it, so the run that claims it next is coded under that policy and is
// not stopped as superseded after it was coded.
func TestARetryAfterTheOwnerChangedThePolicyPinsTheScopeToThePolicyInForce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	failedAttempts(t, f, number)
	f.Cycle(6)
	wantSurfaces(t, f, number, core.StateBlocked)
	if stop := runStopOf(t, f, f.Record(number).RunID); stop.Cause != statemachine.BlockRunExhausted {
		t.Fatalf("the run stopped with cause %s, want it exhausted, the premise of this test", stop.Cause)
	}
	inForce := putPolicyInForce(t, f, "A rule added after the issue was blocked.")

	if _, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f)); err != nil {
		t.Fatalf("the Owner's retry under the policy in force: %v", err)
	}
	if got := f.Record(number).State; got != core.StateReady {
		t.Fatalf("the retried issue is %s, want Ready", got)
	}
	if scope, err := f.Control.Intake.ApprovedScope(ctx, f.Issue(number)); err != nil || scope.PolicyRevision != inForce {
		t.Fatalf("the retried scope is pinned to %s, want the policy in force %s: %v", scope.PolicyRevision, inForce, err)
	}
}

// A scope the Owner approved was approved under the policy then in force.
// Once another policy is in force, the retry of its blocked issue does not
// carry that approval over, whatever stopped the run: the Owner is asked to
// approve the scope under the policy in force first.
func TestARetryAfterTheOwnerChangedThePolicyNeedsAnApprovedScopeApprovedAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("visitor", "NONE", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.Pass()
	if got := f.Record(number).State; got != core.StateApprovalPending {
		t.Fatalf("the outsider's issue is %s, want ApprovalPending, the premise of this test", got)
	}
	approvePending(t, f, number)
	failedAttempts(t, f, number)
	f.Cycle(6)
	wantSurfaces(t, f, number, core.StateBlocked)
	blocked := f.Record(number)
	inForce := putPolicyInForce(t, f, "A rule added after the issue was blocked.")

	_, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f))
	if !core.HasCode(err, core.CodeApprovalRequired) {
		t.Fatalf("the retry carried the approval of the earlier policy over: %v", err)
	}
	if got := f.Record(number); got.State != core.StateBlocked || got.RunID != blocked.RunID {
		t.Fatalf("the refused retry moved the issue: %+v", got)
	}
	f.Clock.Advance(time.Minute)
	approvePending(t, f, number)
	if _, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f)); err != nil {
		t.Fatalf("the Owner's retry after approving the scope under the policy in force: %v", err)
	}
	if scope, err := f.Control.Intake.ApprovedScope(ctx, f.Issue(number)); err != nil || scope.PolicyRevision != inForce {
		t.Fatalf("the retried scope is pinned to %s, want %s: %v", scope.PolicyRevision, inForce, err)
	}
	if got := f.Record(number).State; got != core.StateReady {
		t.Fatalf("the retried issue is %s, want Ready", got)
	}
}

// While the policy the scope was approved under is still in force, the
// retry of a stopped run needs nothing but the Owner asking: the approval
// stands, and the scope is neither judged nor written again.
func TestARetryUnderTheSamePolicyKeepsTheApprovedScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("visitor", "NONE", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.Pass()
	approvePending(t, f, number)
	failedAttempts(t, f, number)
	f.Cycle(6)
	wantSurfaces(t, f, number, core.StateBlocked)
	before, err := f.Control.Intake.ApprovedScope(ctx, f.Issue(number))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f)); err != nil {
		t.Fatalf("the Owner's retry under the policy the scope was approved under: %v", err)
	}
	if got := f.Record(number).State; got != core.StateReady {
		t.Fatalf("the retried issue is %s, want Ready", got)
	}
	after, err := f.Control.Intake.ApprovedScope(ctx, f.Issue(number))
	if err != nil || after.Version != before.Version || after.PolicyRevision != before.PolicyRevision {
		t.Fatalf("the retry wrote the scope again: version %d under %s, was %d under %s: %v",
			after.Version, after.PolicyRevision, before.Version, before.PolicyRevision, err)
	}
}

// rolledBackIssue opens an issue whose first release is rolled back, so it
// is Blocked with both the record of its stopped run and the recovery
// report of the rollback, and returns its number and record.
func rolledBackIssue(t *testing.T, f *harness.Factory) (int, core.StateRecord) {
	t.Helper()
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	f.Deploy.FaultNext("app")
	f.Cycle(6)
	record := f.Record(number)
	if record.State != core.StateBlocked {
		t.Fatalf("the rolled back issue is %s, want Blocked, the premise of this test", record.State)
	}
	if stop := runStopOf(t, f, record.RunID); stop.Cause != statemachine.BlockRolledBack {
		t.Fatalf("the run stopped with cause %s, want it rolled back, the premise of this test", stop.Cause)
	}
	return number, record
}

// rewriteControlRecord changes, or with a nil change removes, a document of
// the control zone, as anyone who writes the control zone's documents
// could, or as a release that kept no record of the stopped run left it.
func rewriteControlRecord[T any](t *testing.T, f *harness.Factory, namespace, key string, change func(*T)) {
	t.Helper()
	err := f.ControlStore.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, namespace, key)
		if err != nil {
			return err
		}
		if change == nil {
			return tx.DeleteKV(ctx, namespace, key, entry.Version)
		}
		var doc T
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			return err
		}
		change(&doc)
		if entry.Value, err = json.Marshal(doc); err != nil {
			return err
		}
		_, err = tx.PutKV(ctx, entry)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// wantRetryRefusedAsBefore checks that the Owner's retry of issue number is
// refused with GUARD_FAILED and reason, and leaves the issue and its scope
// as they were.
func wantRetryRefusedAsBefore(t *testing.T, f *harness.Factory, number int, blocked core.StateRecord, scope core.CommitSHA, reason string) {
	t.Helper()
	ctx := context.Background()
	_, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f))
	if want := core.NewError(core.CodeGuardFailed, reason).Error(); err == nil || err.Error() != want {
		t.Fatalf("the retry: %v, want %s", err, want)
	}
	if got := f.Record(number); got.State != core.StateBlocked || got.Version != blocked.Version {
		t.Fatalf("the refused retry moved the issue: %+v", got)
	}
	if after, err := f.Control.Intake.ApprovedScope(ctx, f.Issue(number)); err != nil || after.PolicyRevision != scope {
		t.Fatalf("the refused retry pinned the scope to %s, was %s: %v", after.PolicyRevision, scope, err)
	}
}

// A rollback recorded before its cause was kept has only its recovery
// report. Its retry after the Owner changed the policy is judged under the
// policy in force as well, and pins the scope to it.
func TestARetryOfARollbackKnownOnlyByItsRecoveryReportPinsTheScopeToThePolicyInForce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number, blocked := rolledBackIssue(t, f)
	rewriteControlRecord[handoff.RunStop](t, f, handoff.BuildNamespace, handoff.RunStopKey(blocked.RunID), nil)
	inForce := putPolicyInForce(t, f, "A rule added after the issue was blocked.")

	if _, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f)); err != nil {
		t.Fatalf("the Owner's retry under the policy in force: %v", err)
	}
	if got := f.Record(number).State; got != core.StateReady {
		t.Fatalf("the retried issue is %s, want Ready", got)
	}
	if scope, err := f.Control.Intake.ApprovedScope(ctx, f.Issue(number)); err != nil || scope.PolicyRevision != inForce {
		t.Fatalf("the retried scope is pinned to %s, want the policy in force %s: %v", scope.PolicyRevision, inForce, err)
	}
}

// A recovery report whose cause is none the factory knows does not say why
// the issue is blocked. A policy change since does not make its retry one
// to judge again: it is refused exactly as without the change.
func TestARetryOfARecoveryReportWithAnUnknownCauseIsRefusedAfterAPolicyChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number, blocked := rolledBackIssue(t, f)
	scope, err := f.Control.Intake.ApprovedScope(ctx, f.Issue(number))
	if err != nil {
		t.Fatal(err)
	}
	rewriteControlRecord[handoff.RunStop](t, f, handoff.BuildNamespace, handoff.RunStopKey(blocked.RunID), nil)
	rewriteControlRecord(t, f, release.RecoveryNamespace, release.RecoveryKey(blocked.RunID), func(r *release.RecoveryReport) {
		r.Cause = "unheard_of"
	})
	const reason = "it is not known why the issue is blocked"
	wantRetryRefusedAsBefore(t, f, number, blocked, scope.PolicyRevision, reason)

	putPolicyInForce(t, f, "A rule added after the issue was blocked.")
	wantRetryRefusedAsBefore(t, f, number, blocked, scope.PolicyRevision, reason)
}

// The record of the issue's run that names another issue is no proof for
// this one. A policy change since does not make its retry one to judge
// again: it is refused exactly as without the change.
func TestARetryOfAStopRecordNamingAnotherIssueIsRefusedAfterAPolicyChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number, blocked := rolledBackIssue(t, f)
	scope, err := f.Control.Intake.ApprovedScope(ctx, f.Issue(number))
	if err != nil {
		t.Fatal(err)
	}
	rewriteControlRecord(t, f, handoff.BuildNamespace, handoff.RunStopKey(blocked.RunID), func(s *handoff.RunStop) {
		s.IssueID = f.Issue(number + 1)
	})
	const reason = "the retry was judged for another issue"
	wantRetryRefusedAsBefore(t, f, number, blocked, scope.PolicyRevision, reason)

	putPolicyInForce(t, f, "A rule added after the issue was blocked.")
	wantRetryRefusedAsBefore(t, f, number, blocked, scope.PolicyRevision, reason)
}
