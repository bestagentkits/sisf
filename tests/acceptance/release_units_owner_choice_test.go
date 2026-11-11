package acceptance

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// The factory never publishes a change to its own dashboard: the page the
// Owner reads is part of the trusted control surface, so the change is
// refused for a person to decide, nothing is deployed and nothing reaches
// main; the issue is blocked for the refusal. A unit release.units does not
// name is refused the same way (unit_not_self_publishable_refused_test.go
// in the release package).
func TestAChangeToTheDashboardIsNotPublished(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	installed := f.Trust.Installed["control"]
	number := f.OpenIssue("maintainer", "MEMBER", "Say who checked the status", dashboardBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, dashboardDiff)
	f.Cycle(6)

	if rec := wantRefusedAndBlocked(t, f, number, core.CodeApprovalRequired); !strings.Contains(rec.Reason, "ui/dashboard/status.html") ||
		!strings.Contains(rec.Reason, "trusted control surface") {
		t.Fatalf("the release was not refused as a change of the trusted control surface: %+v", rec)
	}
	if staged := f.Deploy.Staged(); len(staged) != 0 {
		t.Fatalf("the control unit was deployed: %+v", staged)
	}
	if f.GitData.Merges() != 0 || f.Trust.Installed["control"] != installed {
		t.Fatalf("%d merges reached main; control is %s, was %s", f.GitData.Merges(), f.Trust.Installed["control"], installed)
	}
}
