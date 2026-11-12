package acceptance

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
)

// A held build is not left behind one comment: the Owner is reminded on
// the issue after a day, and again, a bounded number of times.
func TestTheOwnerIsRemindedOfAHeldBuildABoundedNumberOfTimes(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number, _, build := heldBuild(t, f)
	ctx := context.Background()
	f.Pass()
	if commentContaining(t, f, number, "Candidate build held") == "" {
		t.Fatal("no notice of the hold")
	}

	f.Clock.Advance(23 * time.Hour)
	if res := build.Pass(ctx); len(res.Reminded) != 0 {
		t.Fatalf("reminded before a day was over: %+v", res)
	}
	reminders := 0
	for range 5 {
		f.Clock.Advance(24 * time.Hour)
		res := build.Pass(ctx)
		if len(res.Errors) != 0 || !slices.Equal(res.StillHeld, []core.IssueRef{f.Issue(number)}) {
			t.Fatalf("a held build: %+v", res)
		}
		reminders += len(res.Reminded)
	}
	f.Pass()
	if reminders != 2 {
		t.Fatalf("the Owner was reminded %d times, want the 2 allowed", reminders)
	}
	comments, err := f.GitHub.Comments(f.Repo, number)
	if err != nil {
		t.Fatal(err)
	}
	still := 0
	for _, c := range comments {
		if strings.Contains(c.Body, "Candidate build still held") && strings.Contains(c.Body, "factoryd issue retry") {
			still++
		}
	}
	if still != 2 {
		t.Fatalf("%d reminders on the issue, want 2", still)
	}
}
