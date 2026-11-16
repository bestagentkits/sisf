package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// putBuildBase writes the record of the commit to build on as the
// supervisor keeps it, over whatever is stored.
func putBuildBase(t *testing.T, f *harness.Factory, base policy.BuildBase) {
	t.Helper()
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	err = f.ControlStore.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		entry := state.KVEntry{Namespace: policy.BaseNamespace, Key: "build-base", Value: raw}
		if cur, err := tx.GetKV(ctx, policy.BaseNamespace, "build-base"); err == nil {
			entry.Version = cur.Version
		}
		_, err := tx.PutKV(ctx, entry)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// policyRevisionInForce is the revision of the policy the factory follows.
func policyRevisionInForce(t *testing.T, f *harness.Factory) core.CommitSHA {
	t.Helper()
	var revision core.CommitSHA
	err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, "policy", "baseline")
		if err != nil {
			return err
		}
		var b policy.Baseline
		if err := json.Unmarshal(entry.Value, &b); err != nil {
			return err
		}
		revision = b.Revision
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

// The supervisor could not follow a merge on main with the approved content
// unchanged and stopped building on main. No new run is claimed, so
// nothing is coded that could not be released, until the Owner approves a
// policy revision; that approval drops the stop.
func TestNoRunStartsWhileTheFactoryStoppedBuildingOnMain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	revision := policyRevisionInForce(t, f)
	merge := core.CommitSHA("1111111111111111111111111111111111111111")
	putBuildBase(t, f, policy.BuildBase{Revision: revision, Commit: revision,
		Stopped: &policy.BaseStop{Merge: merge, Run: "run-0009", Unit: "app", Reason: "the merge changes AGENTS.md"}})

	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	for range 3 {
		f.Pass()
		f.Clock.Advance(harness.PollInterval)
		res, err := f.Dispatcher.Tick(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Claimed) != 0 {
			t.Fatalf("a run was claimed while the factory stopped building on main: %+v", res)
		}
		if f.Record(number).State == core.StateReady && !core.HasCode(firstError(res.Errors), core.CodeApprovalRequired) {
			t.Fatalf("the brief of a Ready issue was not refused for the Owner's approval: %v", res.Errors)
		}
	}
	if got := f.Record(number); got.State != core.StateReady || got.RunID != "" {
		t.Fatalf("the issue moved while the factory was stopped: %s, run %q", got.State, got.RunID)
	}

	putPolicyInForce(t, f, "The Owner reviewed the change on main.")
	f.Pass()
	err := f.ControlStore.View(ctx, func(ctx context.Context, tx state.Tx) error {
		_, found, err := policy.ReadBuildBase(ctx, tx)
		if err == nil && found {
			return fmt.Errorf("the stop outlived the Owner's approval of a new revision")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// firstError is the first of errs, or nil.
func firstError(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	return errs[0]
}
