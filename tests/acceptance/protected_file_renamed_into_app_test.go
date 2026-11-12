package acceptance

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// thresholdsMovedIntoAppDiff moves the QA thresholds, a protected file,
// into the application's directory unchanged: GitHub's comparison lists it
// as one renamed file.
const thresholdsMovedIntoAppDiff = `--- a/internal/qa/thresholds.txt
+++ /dev/null
@@ -1 +0,0 @@
-pass_score=85
--- /dev/null
+++ b/examples/hello-app/thresholds.txt
@@ -0,0 +1 @@
+pass_score=85
`

// A candidate that moves a protected file into the application's directory
// is refused for the Owner at its release even when the stored list names
// only the path the file reached: GitHub's comparison names the path it
// left as well. Nothing is deployed or merged.
func TestAProtectedFileRenamedIntoTheApplicationIsRefusedAtRelease(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, thresholdsMovedIntoAppDiff)
	untilReleaseCandidate(t, f, number)
	rewriteQAResult(t, f, number, func(r *stages.QAResult) {
		r.Candidate.ChangedPaths = []string{"examples/hello-app/thresholds.txt"}
	})
	f.Cycle(3)

	rec := wantRefusedAndBlocked(t, f, number, core.CodeApprovalRequired)
	if !strings.Contains(rec.Reason, "internal/qa/thresholds.txt") {
		t.Fatalf("the refusal does not name the protected path the file left: %+v", rec)
	}
	if staged := f.Deploy.Staged(); len(staged) != 0 {
		t.Fatalf("the candidate was deployed: %+v", staged)
	}
	wantNotMerged(t, f)
}
