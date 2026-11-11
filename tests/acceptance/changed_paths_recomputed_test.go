package acceptance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// untilReleaseCandidate runs the factory, with the supervisor's build and
// QA stages only, until QA passed the issue's candidate, so a test can act
// between QA and the release.
func untilReleaseCandidate(t *testing.T, f *harness.Factory, number int) {
	t.Helper()
	ctx := context.Background()
	for range 6 {
		f.Pass()
		f.Tick()
		if res := f.Exporter.Pass(ctx); len(res.Errors) > 0 {
			t.Fatalf("exporter: %v", res.Errors)
		}
		if res := f.Stages.Build.Pass(ctx); len(res.Errors) > 0 {
			t.Fatalf("build stage: %v", res.Errors)
		}
		if res := f.Stages.QA.Pass(ctx); len(res.Errors) > 0 {
			t.Fatalf("QA stage: %v", res.Errors)
		}
		f.Pass()
		if f.Record(number).State == core.StateReleaseCandidate {
			return
		}
	}
	t.Fatalf("the candidate did not pass QA: the issue is in %s", f.Record(number).State)
}

// rewriteQAResult changes the QA result of the issue's candidate as anyone
// who writes the control zone's documents could.
func rewriteQAResult(t *testing.T, f *harness.Factory, number int, change func(*stages.QAResult)) {
	t.Helper()
	record := f.Record(number)
	err := f.ControlStore.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		key := stages.QAResultKey(record.RunID, record.Attempt)
		entry, err := tx.GetKV(ctx, stages.Namespace, key)
		if err != nil {
			return err
		}
		var result stages.QAResult
		if err := json.Unmarshal(entry.Value, &result); err != nil {
			return err
		}
		change(&result)
		if entry.Value, err = json.Marshal(result); err != nil {
			return err
		}
		_, err = tx.PutKV(ctx, entry)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The paths a candidate changes are listed again from GitHub when it is
// released, so a stored list that leaves a path out decides nothing: the
// candidate that also changes a path its unit does not consume is refused
// for the Owner, and nothing is deployed or merged.
func TestAStoredListOfChangedPathsThatLeavesOneOutDoesNotDecideTheRelease(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingAndDocsDiff)
	untilReleaseCandidate(t, f, number)
	rewriteQAResult(t, f, number, func(r *stages.QAResult) {
		r.Candidate.ChangedPaths = []string{"examples/hello-app/main.go"}
	})
	f.Cycle(3)

	rec := wantRefusedAndBlocked(t, f, number, core.CodeApprovalRequired)
	if !strings.Contains(rec.Reason, "docs/greeting.md") {
		t.Fatalf("the refusal does not name the path GitHub lists: %+v", rec)
	}
	if staged := f.Deploy.Staged(); len(staged) != 0 {
		t.Fatalf("the candidate was deployed: %+v", staged)
	}
	wantNotMerged(t, f)
}

// A comparison GitHub cuts short when the candidate is released refuses it
// for the Owner, whatever QA's stored list says about its completeness.
func TestAComparisonCutShortAtTheReleaseRefusesTheCandidate(t *testing.T) {
	t.Parallel()
	f, number := greetingIssue(t)
	untilReleaseCandidate(t, f, number)
	f.GitData.CompareBeyondTheLimit()
	f.Cycle(3)

	wantRefusedAndBlocked(t, f, number, core.CodeApprovalRequired)
	if staged := f.Deploy.Staged(); len(staged) != 0 {
		t.Fatalf("the candidate was deployed: %+v", staged)
	}
	wantNotMerged(t, f)
}
