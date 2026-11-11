package acceptance

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/actions/runactions"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
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
