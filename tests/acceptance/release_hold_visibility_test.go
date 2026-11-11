package acceptance

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/actions/runactions"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// A release the supervisor holds, here because no real traffic judges it
// and the application has no synthetic suite, is impossible to miss: the
// supervisor's record keeps its code and reason, runs.explain reports the
// run stopped with the reason as evidence and the Owner's retry as the way
// out, runs.inspect shows the hold, and the issue's progress comment says
// the release is waiting in words safe to publish. It is not tried again
// by itself. Only the Owner's retry lets it start over; once the release
// can be judged it goes ahead and the issue closes.
func TestAHeldReleaseIsShownAndTheOwnersRetryReleasesIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	f.Quiet()
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	f.Cycle(4)
	wantSurfaces(t, f, number, core.StateReleaseCandidate)
	record := f.Record(number)

	rec := releaseRecordOf(t, f, record)
	if rec.Result != "held" || rec.Code != string(core.CodeReleaseHold) || !strings.Contains(rec.Reason, "real traffic is too low") {
		t.Fatalf("the release record: %+v", rec)
	}
	hold := releaseHoldOf(t, f, record)
	if hold.Code != core.CodeReleaseHold || !hold.Waiting() {
		t.Fatalf("the release hold: %+v", hold)
	}

	comment := commentContaining(t, f, number, "<!-- sisf:progress ")
	if !strings.Contains(comment, "**Release waiting:**") || !strings.Contains(comment, hold.Summary()) || strings.Contains(comment, "real traffic") {
		t.Errorf("the progress comment does not say the release waits, in public words:\n%s", comment)
	}
	why := f.Explain(number)
	if !why.Stopped || why.Cause != runactions.CauseReleaseHeld || !strings.Contains(why.Reason, "held") || !hasEvidence(why, "release_hold") ||
		!strings.Contains(why.Reason, "factoryd issue retry --issue "+f.Issue(number).String()) {
		t.Errorf("runs.explain answers %+v", why)
	}
	if !slices.ContainsFunc(why.Evidence, func(e runactions.Evidence) bool {
		return e.Kind == "release_hold" && strings.Contains(e.Summary, "real traffic is too low")
	}) {
		t.Errorf("runs.explain does not give the supervisor's reason: %+v", why.Evidence)
	}
	if seen := f.Inspect(number); seen.ReleaseHold == nil || seen.ReleaseHold.Code != string(core.CodeReleaseHold) {
		t.Errorf("runs.inspect shows the hold %+v", seen.ReleaseHold)
	}

	// Nothing tries it again by itself.
	f.Cycle(2)
	if again := releaseRecordOf(t, f, record); !again.At.Equal(rec.At) || len(f.Deploy.Staged()) != 0 {
		t.Fatalf("a held release was tried again by itself: %+v", again)
	}

	// Only the Owner resumes it.
	maintainer := core.Actor{ID: "user:maintainer", Kind: core.ActorHuman, Roles: []core.Role{core.RoleMaintainer}, Surface: core.SurfaceCLI}
	if _, err := f.Control.Retry(ctx, f.Issue(number), maintainer); !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("a maintainer resumed the held release: %v", err)
	}
	owner := core.Actor{ID: "owner:local", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner}, Surface: core.SurfaceCLI, StepUpAt: f.Clock.Now()}
	if result, err := f.Control.Retry(ctx, f.Issue(number), owner); err != nil || result.State != core.StateReleaseCandidate {
		t.Fatalf("the Owner's retry: %+v, %v", result, err)
	}
	if _, err := f.Control.Retry(ctx, f.Issue(number), owner); !core.HasCode(err, core.CodeConflict) {
		t.Fatalf("a release already resumed was resumed again: %v", err)
	}

	// The users are back, so the release can be judged, and it goes ahead.
	f.Busy()
	f.Cycle(4)
	wantSurfaces(t, f, number, core.StateClosed)
	if rec := releaseRecordOf(t, f, record); rec.Result != "closed" || rec.Code != "" {
		t.Fatalf("the resumed release: %+v", rec)
	}
	err := f.ControlStore.View(ctx, func(ctx context.Context, tx state.Tx) error {
		_, err := handoff.ReadReleaseHold(ctx, tx, record.RunID, record.Attempt)
		return err
	})
	if !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("the hold of a release that went ahead is still recorded: %v", err)
	}
	if c := commentContaining(t, f, number, "<!-- sisf:progress "); strings.Contains(c, "Release waiting") {
		t.Errorf("the closed issue still says the release waits:\n%s", c)
	}
}

func releaseRecordOf(t *testing.T, f *harness.Factory, r core.StateRecord) stages.ReleaseRecord {
	t.Helper()
	var rec stages.ReleaseRecord
	err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		rec, err = stages.ReadRelease(ctx, tx, r.RunID, r.Attempt)
		return err
	})
	if err != nil {
		t.Fatalf("read the release record: %v", err)
	}
	return rec
}

// wantRefusedAndBlocked fails unless the supervisor refused the release of
// issue number with code and blocked the issue for it: the record is
// Blocked for the run whose release was refused, and the run's stop record
// names the refusal and its code. It returns the record of the refused
// release.
func wantRefusedAndBlocked(t *testing.T, f *harness.Factory, number int, code core.ErrorCode) stages.ReleaseRecord {
	t.Helper()
	record := f.Record(number)
	if record.State != core.StateBlocked {
		t.Fatalf("the refused release left the issue in %s, want Blocked", record.State)
	}
	stop := runStopOf(t, f, record.RunID)
	if stop.Cause != statemachine.BlockReleaseRefused || stop.ReleaseCode != code {
		t.Fatalf("the issue is not blocked for the refusal with %s: %+v", code, stop)
	}
	rec := releaseRecordOf(t, f, record)
	if rec.Result != string(release.ResultRefused) || !containsCode(rec.Reason, code) {
		t.Fatalf("the release was not refused with %s: %+v", code, rec)
	}
	return rec
}

func releaseHoldOf(t *testing.T, f *harness.Factory, r core.StateRecord) handoff.ReleaseHold {
	t.Helper()
	var h handoff.ReleaseHold
	err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		h, err = handoff.ReadReleaseHold(ctx, tx, r.RunID, r.Attempt)
		return err
	})
	if err != nil {
		t.Fatalf("read the release hold: %v", err)
	}
	return h
}

// A release the supervisor refuses, here because release.units does not
// let the factory publish the unit by itself, does not leave its issue
// waiting for a release that never comes: the supervisor blocks the issue,
// and runs.explain, runs.inspect and the issue's progress comment say why
// in words chosen by the cause and the code alone, with what the Owner can
// do. The refused candidate is never tried again; only the Owner's retry
// returns the issue to Ready, for a new run, and a maintainer's is denied.
func TestARefusedReleaseBlocksItsIssueUntilTheOwnerRetriesIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Say who checked the status", dashboardBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, dashboardDiff)
	f.Cycle(6)
	wantSurfaces(t, f, number, core.StateBlocked)
	wantLabels(t, f, number, github.LabelBlocked)
	rec := wantRefusedAndBlocked(t, f, number, core.CodeApprovalRequired)
	record := f.Record(number)
	stop := runStopOf(t, f, record.RunID)
	retry := "factoryd issue retry --issue " + f.Issue(number).String()

	comment := commentContaining(t, f, number, "<!-- sisf:progress ")
	if !strings.Contains(comment, "**Blocked:**") || !strings.Contains(comment, stop.Summary()) || !strings.Contains(comment, retry) ||
		strings.Contains(comment, "Release waiting") || strings.Contains(comment, "release.units:") {
		t.Errorf("the progress comment does not say the release was refused and the issue blocked, in public words:\n%s", comment)
	}
	why := f.Explain(number)
	if !why.Stopped || why.Cause != runactions.CauseReleaseRefused || !strings.Contains(why.Reason, retry) ||
		!strings.Contains(why.Reason, "published only by a person") || !hasEvidence(why, "run_stop") {
		t.Errorf("runs.explain answers %+v", why)
	}
	if seen := f.Inspect(number); seen.ReleaseHold != nil {
		t.Errorf("runs.inspect shows a hold for a blocked issue: %+v", seen.ReleaseHold)
	}

	// Nothing tries the refused candidate again.
	f.Cycle(2)
	if again := releaseRecordOf(t, f, record); !again.At.Equal(rec.At) || len(f.Deploy.Staged()) != 0 {
		t.Fatalf("a refused release was tried again: %+v", again)
	}
	if got := f.Record(number); got.State != core.StateBlocked || got.Version != record.Version {
		t.Fatalf("the blocked issue moved by itself: %+v", got)
	}

	// Only the Owner's retry returns the issue to Ready, once.
	maintainer := core.Actor{ID: "user:maintainer", Kind: core.ActorHuman, Roles: []core.Role{core.RoleMaintainer}, Surface: core.SurfaceCLI}
	if _, err := f.Control.Retry(ctx, f.Issue(number), maintainer); !core.HasCode(err, core.CodeGuardFailed) {
		t.Fatalf("a maintainer retried the refused release: %v", err)
	}
	if _, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f)); err != nil {
		t.Fatalf("the Owner's retry: %v", err)
	}
	ready := f.Record(number)
	if ready.State != core.StateReady {
		t.Fatalf("the retried issue is %s, want Ready", ready.State)
	}
	if _, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f)); !core.HasCode(err, core.CodeInvalidTransition) {
		t.Fatalf("a second retry of the same block was not refused cleanly: %v", err)
	}
	if got := f.Record(number); got.State != ready.State || got.Version != ready.Version {
		t.Fatalf("the second retry moved the issue: %+v", got)
	}
}

// A release held while its receipt was valid and resumed by the Owner
// after the receipt expired is refused for that, not for a policy, and
// blocks its issue with a summary that says the receipt expired; the
// Owner's retry returns the issue to Ready for a new run.
func TestAHeldReleaseResumedAfterItsReceiptExpiredBlocksItsIssue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	f.Quiet()
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	f.Cycle(4)
	wantSurfaces(t, f, number, core.StateReleaseCandidate)
	if hold := releaseHoldOf(t, f, f.Record(number)); !hold.Waiting() {
		t.Fatalf("the release is not held: %+v", hold)
	}

	f.Clock.Advance(25 * time.Hour)
	if _, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f)); err != nil {
		t.Fatalf("the Owner's resumption of the held release: %v", err)
	}
	f.Busy()
	f.Cycle(2)
	// The dashboard's session lapsed with the clock, so the record alone is read.
	wantRefusedAndBlocked(t, f, number, core.CodeExpired)
	if staged := f.Deploy.Staged(); len(staged) != 0 {
		t.Fatalf("an expired candidate was deployed: %+v", staged)
	}
	stop := runStopOf(t, f, f.Record(number).RunID)
	if summary := stop.Summary(); !strings.Contains(summary, "expired") || strings.Contains(summary, "policy") {
		t.Fatalf("the summary of an expired receipt: %q", summary)
	}
	comment := commentContaining(t, f, number, "<!-- sisf:progress ")
	if !strings.Contains(comment, "**Blocked:**") || !strings.Contains(comment, "expired") {
		t.Errorf("the progress comment does not say the receipt expired:\n%s", comment)
	}

	maintainer := core.Actor{ID: "user:maintainer", Kind: core.ActorHuman, Roles: []core.Role{core.RoleMaintainer}, Surface: core.SurfaceCLI}
	if _, err := f.Control.Retry(ctx, f.Issue(number), maintainer); !core.HasCode(err, core.CodeGuardFailed) {
		t.Fatalf("a maintainer retried the blocked issue: %v", err)
	}
	if _, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f)); err != nil {
		t.Fatalf("the Owner's retry: %v", err)
	}
	ready := f.Record(number)
	if ready.State != core.StateReady {
		t.Fatalf("the retried issue is %s, want Ready", ready.State)
	}
	if _, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f)); !core.HasCode(err, core.CodeInvalidTransition) {
		t.Fatalf("a second retry of the same block was not refused cleanly: %v", err)
	}
	if got := f.Record(number); got.Version != ready.Version {
		t.Fatalf("the second retry moved the issue: %+v", got)
	}
}

// A held release whose record cannot be read is not retried as if nothing
// were held: the Owner's retry reports the record as tampered with and
// moves nothing.
func TestTheRetryOfAReleaseWhoseHoldCannotBeReadSaysSo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	f.Quiet()
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	f.Cycle(4)
	record := f.Record(number)
	if record.State != core.StateReleaseCandidate {
		t.Fatalf("the release is not held: %+v", record)
	}
	key := handoff.ReleaseHoldKey(record.RunID, record.Attempt)
	if err := f.ControlStore.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, handoff.BuildNamespace, key)
		if err != nil {
			return err
		}
		entry.Value = []byte(`{"run_id":"run-elsewhere"}`)
		_, err = tx.PutKV(ctx, entry)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f)); !core.HasCode(err, core.CodeTampered) {
		t.Fatalf("the retry of a release whose hold cannot be read: %v", err)
	}
	if got := f.Record(number); got.State != record.State || got.Version != record.Version {
		t.Fatalf("the retry moved the issue: %+v", got)
	}
}
