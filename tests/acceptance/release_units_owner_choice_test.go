package acceptance

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// A manifest whose release.units names the application alone lets the
// factory publish nothing else: a change to its own dashboard, a build of
// the control unit, is refused for a person to decide, nothing is deployed
// and nothing reaches main.
func TestAUnitTheManifestDoesNotNameIsNotPublished(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	installed := f.Trust.Installed["control"]
	number := f.OpenIssue("maintainer", "MEMBER", "Say who checked the status", dashboardBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, dashboardDiff)
	f.Cycle(6)

	record := f.Record(number)
	if record.State != core.StateReleaseCandidate {
		t.Fatalf("the issue went on to %s", record.State)
	}
	var rec stages.ReleaseRecord
	err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		rec, err = stages.ReadRelease(ctx, tx, record.RunID, record.Attempt)
		return err
	})
	if err != nil || rec.Result != string(release.ResultRefused) || !containsCode(rec.Reason, core.CodeApprovalRequired) || !strings.Contains(rec.Reason, "release.units") {
		t.Fatalf("the release was not refused by release.units: %+v, %v", rec, err)
	}
	if staged := f.Deploy.Staged(); len(staged) != 0 {
		t.Fatalf("the control unit was deployed: %+v", staged)
	}
	if f.GitData.Merges() != 0 || f.Trust.Installed["control"] != installed {
		t.Fatalf("%d merges reached main; control is %s, was %s", f.GitData.Merges(), f.Trust.Installed["control"], installed)
	}
}
