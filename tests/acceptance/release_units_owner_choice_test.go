package acceptance

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// A manifest whose release.units names the application alone lets the
// factory publish nothing else: a change to its own dashboard, a build of
// the control unit, is refused for a person to decide, nothing is deployed
// and nothing reaches main; the issue is blocked for the refusal.
func TestAUnitTheManifestDoesNotNameIsNotPublished(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	installed := f.Trust.Installed["control"]
	number := f.OpenIssue("maintainer", "MEMBER", "Say who checked the status", dashboardBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, dashboardDiff)
	f.Cycle(6)

	if rec := wantRefusedAndBlocked(t, f, number, core.CodeApprovalRequired); !strings.Contains(rec.Reason, "release.units") {
		t.Fatalf("the release was not refused by release.units: %+v", rec)
	}
	if staged := f.Deploy.Staged(); len(staged) != 0 {
		t.Fatalf("the control unit was deployed: %+v", staged)
	}
	if f.GitData.Merges() != 0 || f.Trust.Installed["control"] != installed {
		t.Fatalf("%d merges reached main; control is %s, was %s", f.GitData.Merges(), f.Trust.Installed["control"], installed)
	}
}
