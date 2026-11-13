package acceptance

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state"
)

// sitePageOriginalRewordDiff words the status page differently, against
// the page as the trust baseline the factory was installed on holds it.
const sitePageOriginalRewordDiff = `--- a/site/status.html
+++ b/site/status.html
@@ -1,2 +1,2 @@
 <h1>Factory status</h1>
-<p>All systems nominal.</p>
+<p>Every system is working.</p>
`

// The factory released and merged a change of a unit, but the commit to
// build on did not follow it (this harness keeps no audit log, so the
// supervisor never moves it). A second candidate of that unit is built on
// a base without the first change: releasing it would serve, and merge, a
// unit without that change. It is held right after its build, so it is
// never judged or deployed, its issue is blocked for the Owner with the
// reason named, and the last known good release keeps serving.
func TestASecondReleaseOfAUnitOnABaselineWithoutTheFirstIsStoppedBeforeQA(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{ReleaseUnits: []manifest.UnitName{manifest.UnitApp, manifest.UnitControl}, UnitInputs: controlSitePage})

	first := f.OpenIssue("maintainer", "MEMBER", "Say who checked the status", dashboardBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(first, 0, sitePageDiff)
	f.Cycle(4)
	wantSurfaces(t, f, first, core.StateClosed)
	good, found, err := f.Trust.LKG.Get(ctx, "control")
	if err != nil || !found || good.Commit == "" {
		t.Fatalf("the first release is not the last known good, with its commit: %+v, %v", good, err)
	}
	staged := len(f.Deploy.Staged())

	second := f.OpenIssue("maintainer", "MEMBER", "Reword the status page", dashboardBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(second, 0, sitePageOriginalRewordDiff)
	record := exportedPatch(t, f, second)
	res := f.Stages.Build.Pass(ctx)
	if !slices.Equal(res.Held, []core.IssueRef{f.Issue(second)}) || len(res.Submitted) != 0 || len(res.Unbuildable) != 0 {
		t.Fatalf("the build of a candidate whose base lacks the last known good release was not held: %+v", res)
	}
	if !strings.Contains(fmt.Sprint(res.Errors), string(good.Commit)) {
		t.Fatalf("the hold does not name the commit of the last known good release: %v", res.Errors)
	}
	err = f.ControlStore.View(ctx, func(ctx context.Context, tx state.Tx) error {
		_, err := handoff.ReadCandidate(ctx, tx, record.RunID, record.Attempt)
		return err
	})
	if !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("a candidate that could not be released was sent to QA: %v", err)
	}
	if held := heldListing(t, f); len(held) != 1 || held[0].Reason != handoff.HeldBaseLacksRelease {
		t.Fatalf("the held build does not say its base lacks the release: %+v", held)
	}

	f.Pass()
	wantSurfaces(t, f, second, core.StateBlocked)
	stop := runStopOf(t, f, record.RunID)
	if stop.Cause != statemachine.BlockPolicySuperseded || stop.Held != handoff.HeldBaseLacksRelease {
		t.Fatalf("the record of the stopped run: %+v", stop)
	}
	if why := f.Explain(second); !why.Stopped || !strings.Contains(why.Reason, "last known good release") {
		t.Errorf("runs.explain answers %+v", why)
	}
	if now := f.Deploy.Staged(); len(now) != staged {
		t.Fatalf("the second candidate was deployed: %+v", now[staged:])
	}
	if observed, err := f.Deploy.ObservedRevision(ctx, "control"); err != nil || !observed.Healthy || observed.Digest != good.Digest {
		t.Fatalf("the control unit runs %+v, %v; want the last known good release", observed, err)
	}
}
