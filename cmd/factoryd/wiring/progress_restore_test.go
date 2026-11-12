package wiring

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// A restore moves an issue to RolledBack and then, in a second step, to
// Blocked with its recovery report. A progress comment written between the
// two still follows the issue to Blocked: RolledBack is not where the run
// ends.
func TestTheProgressOfARollbackFollowsTheIssueToBlocked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, gh, _ := closeGateControl(t)
	issue := core.IssueRef{Owner: "acme", Repo: "app", Number: 8}
	r := core.StateRecord{IssueID: issue, State: core.StateMonitoring, RunID: "run-1", Version: 1}
	for _, s := range []core.State{core.StateMonitoring, core.StateRolledBack, core.StateBlocked} {
		r.State, r.Version = s, r.Version+1
		if err := c.projectIssue(ctx, r); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	_, _, events := gh.state()
	if want := "comment Monitoring,comment RolledBack,comment Blocked"; strings.Join(events, ",") != want {
		t.Fatalf("GitHub saw %v, want %s", events, want)
	}
	if !c.progressGivenUp(ctx, issue) {
		t.Fatal("the progress of a run that ended in Blocked is still followed")
	}
}
