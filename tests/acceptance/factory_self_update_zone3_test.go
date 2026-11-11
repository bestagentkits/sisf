package acceptance

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

const dashboardBody = "The status page should say when it was checked.\n\n### Definition of Done\n- the status page names the factory as its checker\n"

// The factory improves a page of its own control unit that the Owner
// declared an input of control: the supervisor builds and releases a new
// control unit, and when a later build of it, made once the Owner put the
// merged release in force, breaks, rolls it back to the last known good one
// and reports why on the issue. The application keeps
// serving throughout and the supervisor is never replaced.
func TestTheFactoryUpdatesItsDashboardAndRollsABrokenBuildBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{ReleaseUnits: []manifest.UnitName{manifest.UnitApp, manifest.UnitControl}, UnitInputs: controlSitePage})
	installed := f.Trust.Installed["control"]

	first := f.OpenIssue("maintainer", "MEMBER", "Say who checked the status", dashboardBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(first, 0, sitePageDiff)
	f.Cycle(4)
	wantSurfaces(t, f, first, core.StateClosed)
	good, found, err := f.Trust.LKG.Get(ctx, "control")
	if err != nil || !found || good.Digest == installed || core.Digest(good.Digest) != f.Record(first).CandidateDigest {
		t.Fatalf("the new control unit is not the last known good: %+v, %v", good, err)
	}

	// The factory merged the first release; the Owner moves the trust
	// baseline to the default branch that now holds it, as a candidate of
	// the control unit built on a base without it is refused.
	head, _, ok := f.GitData.Branch(f.Repo, "main")
	if !ok {
		t.Fatal("the default branch is not known")
	}
	approveRevision(t, f, core.CommitSHA(head))

	second := f.OpenIssue("maintainer", "MEMBER", "Reword the status page", dashboardBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(second, 0, sitePageRewordDiff)
	f.Deploy.FaultNext("control")
	f.Cycle(4)
	wantSurfaces(t, f, second, core.StateBlocked)
	wantJourneyEnds(t, f, second, []core.State{core.StateReleaseCandidate, core.StateCanary, core.StateRolledBack, core.StateBlocked})
	wantLabels(t, f, second, github.LabelBlocked)
	if after, _, err := f.Trust.LKG.Get(ctx, "control"); err != nil || after.Digest != good.Digest {
		t.Fatalf("the last known good moved to %+v, %v", after, err)
	}
	if observed, err := f.Deploy.ObservedRevision(ctx, "control"); err != nil || !observed.Healthy || observed.Digest != good.Digest {
		t.Fatalf("after the rollback the control unit runs %+v, %v", observed, err)
	}
	if report := commentContaining(t, f, second, "Release rolled back"); report == "" {
		t.Fatal("no recovery report was posted on the issue")
	}
	// The application served every request while the factory replaced itself.
	if sent, failed := f.Probed("app"); sent == 0 || failed != 0 {
		t.Fatalf("the application failed %d of %d requests", failed, sent)
	}
	// The supervisor was never staged, and the release it pins is unchanged.
	for _, rev := range f.Deploy.Staged() {
		if rev.Unit == "supervisor" {
			t.Fatalf("the supervisor was redeployed: %+v", rev)
		}
	}
	if sup, found, err := f.Trust.LKG.Get(ctx, "supervisor"); err != nil || !found || core.Digest(sup.Digest) != f.Trust.Supervisor {
		t.Fatalf("the supervisor's pinned release changed: %+v, %v", sup, err)
	}
}

// A candidate that changes the trusted control surface (here the QA
// baseline) is a governance change: the supervisor refuses to release it,
// whatever QA said, blocks the issue for it, and nothing is deployed.
func TestACandidateThatTouchesTheQABaselineIsRefusedAsAGovernanceChange(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Make QA faster", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, qaBaselineDiff)
	f.Cycle(4)

	wantRefusedAndBlocked(t, f, number, core.CodeApprovalRequired)
	if staged := f.Deploy.Staged(); len(staged) != 0 {
		t.Fatalf("a governance change was deployed: %+v", staged)
	}
}

func containsCode(reason string, code core.ErrorCode) bool {
	return len(reason) >= len(code) && reason[:len(code)] == string(code)
}
