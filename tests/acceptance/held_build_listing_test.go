package acceptance

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// heldBuild takes a fresh issue to a candidate build held after outages.
func heldBuild(t *testing.T, f *harness.Factory) (int, core.StateRecord, *stages.BuildStage) {
	t.Helper()
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	record := exportedPatch(t, f, number)
	build := f.Stages.Build
	build.Builder.Git = &failingCommits{Repository: build.Builder.Git, fail: 100}
	build.Retry = stages.BuildRetry{First: time.Minute, Max: time.Minute, HoldAfter: 1}
	if res := build.Pass(context.Background()); !slices.Equal(res.Held, []core.IssueRef{f.Issue(number)}) {
		t.Fatalf("the build was not held: %+v", res)
	}
	return number, record, build
}

func heldListing(t *testing.T, f *harness.Factory) []handoff.BuildOutage {
	t.Helper()
	var held []handoff.BuildOutage
	err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		held, err = handoff.ListHeldBuilds(ctx, tx)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return held
}

// A build held by a release that kept no listing of held builds is put on
// the listing by the next pass, so the supervisor's status shows it.
func TestABuildHeldBeforeTheListingExistedIsListed(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	_, record, build := heldBuild(t, f)
	ctx := context.Background()
	if held := heldListing(t, f); len(held) != 1 || held[0].RunID != record.RunID {
		t.Fatalf("the held build is not listed: %+v", held)
	}
	// As an earlier release left it: the outage record, no listing.
	err := f.ControlStore.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		entries, err := tx.ListKV(ctx, handoff.BuildNamespace, "build-held/", 0)
		for _, e := range entries {
			if err := tx.DeleteKV(ctx, handoff.BuildNamespace, e.Key, e.Version); err != nil {
				return err
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if held := heldListing(t, f); len(held) != 0 {
		t.Fatalf("the listing was not removed: %+v", held)
	}
	build.Pass(ctx)
	if held := heldListing(t, f); len(held) != 1 || held[0].RunID != record.RunID {
		t.Fatalf("the pass did not list the held build: %+v", held)
	}
}
