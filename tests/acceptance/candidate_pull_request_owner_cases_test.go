//go:build unix

package acceptance

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state"
)

// adrDiff adds an architecture decision record: a foundation file.
const adrDiff = `--- /dev/null
+++ b/docs/adr/0099-greeting.md
@@ -0,0 +1 @@
+# ADR 0099: Greet the world
`

// A candidate that changes a foundation file is never merged by the
// factory: release governance refuses to release it, so it never reaches
// Verified, and its pull request stays open for the Owner.
func TestACandidateThatChangesAFoundationFileIsLeftForTheOwner(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Record the greeting decision", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, adrDiff)
	f.Cycle(4)

	record := f.Record(number)
	if record.State != core.StateReleaseCandidate {
		t.Fatalf("the foundation change went on to %s", record.State)
	}
	var rec stages.ReleaseRecord
	if err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		rec, err = stages.ReadRelease(ctx, tx, record.RunID, record.Attempt)
		return err
	}); err != nil || rec.Result != string(release.ResultRefused) || !containsCode(rec.Reason, core.CodeApprovalRequired) {
		t.Fatalf("the release was not refused as a governance change: %+v, %v", rec, err)
	}
	pr := candidatePR(t, f, number)
	if pr.State.Final() || f.GitData.Merges() != 0 {
		t.Fatalf("the foundation change's pull request: %+v after %d merges", pr, f.GitData.Merges())
	}
	if _, files, _ := f.GitData.Branch(f.Repo, "main"); files["docs/adr/0099-greeting.md"] != "" {
		t.Fatal("the foundation change reached main")
	}
	if view, err := f.GitHub.Issue(f.Repo, pr.Number); err != nil || view.State != "open" {
		t.Fatalf("the pull request is %q, %v", view.State, err)
	}
}

// Two issues in flight: the first merge moves main, and the second
// candidate, judged against the same approved policy, is merged too.
func TestTwoVerifiedCandidatesAreBothMerged(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{ReleaseUnits: []manifest.UnitName{manifest.UnitApp, manifest.UnitControl}})
	greeting := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(greeting, 0, greetingDiff)
	dashboard := f.OpenIssue("maintainer", "MEMBER", "Say when the status was checked", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(dashboard, 0, dashboardDiff)
	for range 10 {
		if f.Cycle(1); f.Record(greeting).State == core.StateClosed && f.Record(dashboard).State == core.StateClosed {
			break
		}
	}
	for _, number := range []int{greeting, dashboard} {
		wantSurfaces(t, f, number, core.StateClosed)
		if pr := candidatePR(t, f, number); pr.State != handoff.LandingMerged {
			t.Errorf("the pull request of #%d: %+v", number, pr)
		}
	}
	_, files, _ := f.GitData.Branch(f.Repo, "main")
	if f.GitData.Merges() != 2 || files["app/main.go"] != greetedMain || !strings.Contains(files["ui/dashboard/status.html"], "Last checked by the factory") {
		t.Fatalf("main after %d merges: %v", f.GitData.Merges(), files)
	}
}
