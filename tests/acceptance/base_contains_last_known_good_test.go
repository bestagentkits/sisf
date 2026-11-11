package acceptance

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/manifest"
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

// The factory released and merged a change of a unit. A second candidate
// of that unit is built on the trust baseline in force, which the Owner
// has not moved to a commit that holds the first change: releasing it
// would serve, and merge, a unit without that change. It is refused for the
// Owner before anything is deployed, the reason names the commit of the
// last known good release, and the last known good release keeps serving.
func TestASecondReleaseOfAUnitOnABaselineWithoutTheFirstIsRefused(t *testing.T) {
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
	f.Cycle(4)
	rec := wantRefusedAndBlocked(t, f, second, core.CodeApprovalRequired)
	if !strings.Contains(rec.Reason, string(good.Commit)) {
		t.Fatalf("the refusal does not name the commit of the last known good release: %+v", rec)
	}
	if now := f.Deploy.Staged(); len(now) != staged {
		t.Fatalf("the second candidate was deployed: %+v", now[staged:])
	}
	if observed, err := f.Deploy.ObservedRevision(ctx, "control"); err != nil || !observed.Healthy || observed.Digest != good.Digest {
		t.Fatalf("the control unit runs %+v, %v; want the last known good release", observed, err)
	}
}
