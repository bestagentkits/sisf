package policy_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
)

// checkMain asks whether the release of run may be merged into main at
// head while landings are in flight.
func (w *baseWorld) checkMain(head core.CommitSHA, run core.RunID, landings ...policy.FollowProof) error {
	return w.bases.CheckMain(context.Background(), policy.SupervisorActor(),
		policy.MainCheck{Head: head, Run: run, Unit: "hello-app", Landings: landings})
}

// Two releases finish in the same pass. The first is merged, but its
// follow meets an outage; the second must not be merged on top of a merge
// the factory has not followed, or following it would stop the factory.
// The check of main follows the first merge, or waits while it cannot.
func TestAMergeWhoseFollowLagsIsFollowedBeforeAnotherIsMadeOnIt(t *testing.T) {
	t.Parallel()
	app := []string{"examples/hello-app/main.go"}

	t.Run("the follow of the first merge met an outage", func(t *testing.T) {
		t.Parallel()
		w := newBaseWorld(t)
		w.release(cand1, merge1, revA, revA, app, nil)
		w.history.down = true
		_, err := w.follow(merge1, cand1, "run-1")
		wantCode(t, err, core.CodeUnavailable, "following while GitHub is out")
		first := policy.FollowProof{Merge: merge1, Candidate: cand1, Run: "run-1", Unit: "hello-app"}

		// GitHub is still out when the second release is about to merge:
		// it waits, and nothing stops.
		err = w.checkMain(merge1, "run-2", first)
		wantCode(t, err, core.CodeUnavailable, "merging on an unfollowed merge during an outage")
		if !strings.Contains(err.Error(), "run-1, which the factory has not followed yet") {
			t.Fatalf("the wait does not name the release whose follow it waits on: %v", err)
		}
		if _, found := w.stored(t); found {
			t.Fatal("an outage while main was checked stopped the factory")
		}

		w.history.down = false
		if err := w.checkMain(merge1, "run-2", first); err != nil {
			t.Fatalf("main at the first release's merge was refused: %v", err)
		}
		if base, _ := w.stored(t); base.Commit != merge1 || base.Stopped != nil {
			t.Fatalf("the first merge was not followed before the second was made on it: %+v", base)
		}
		w.release(cand2, merge2, merge1, merge1, app, nil)
		if base, err := w.follow(merge2, cand2, "run-2"); err != nil || base.Commit != merge2 || base.Stopped != nil {
			t.Fatalf("the second release's merge was not followed: %+v, %v", base, err)
		}
		// The first release's own follow, tried again, changes nothing.
		if base, err := w.follow(merge1, cand1, "run-1"); err != nil || base.Commit != merge2 {
			t.Fatalf("the first follow tried again = %+v, %v", base, err)
		}
	})

	t.Run("the supervisor restarted between the merge and its record", func(t *testing.T) {
		t.Parallel()
		w := newBaseWorld(t)
		w.release(cand1, merge1, revA, revA, app, nil)
		// The merge commit was never recorded: only the candidate is known.
		pending := policy.FollowProof{Candidate: cand1, Run: "run-1", Unit: "hello-app"}
		if err := w.checkMain(merge1, "run-2", pending); err != nil {
			t.Fatalf("main at a merge made before a restart was refused: %v", err)
		}
		base, _ := w.stored(t)
		if base.Commit != merge1 || base.Run != "run-1" || base.Stopped != nil {
			t.Fatalf("the merge made before the restart was not followed as run-1's: %+v", base)
		}
	})

	t.Run("a lagging follow still refused stops with its own reason", func(t *testing.T) {
		t.Parallel()
		w := newBaseWorld(t)
		w.release(cand1, merge1, revA, revA, []string{".github/workflows/release.yml"}, nil)
		err := w.checkMain(merge1, "run-2", policy.FollowProof{Merge: merge1, Candidate: cand1, Run: "run-1"})
		wantCode(t, err, core.CodeApprovalRequired, "merging on a merge that changes CI")
		if base, _ := w.stored(t); base.Stopped == nil || base.Stopped.Merge != merge1 || !strings.Contains(base.Stopped.Reason, ".github/workflows/release.yml") {
			t.Fatalf("the stop does not name the merge and its cause: %+v", base.Stopped)
		}
	})

	t.Run("a commit the factory did not make still stops the follow", func(t *testing.T) {
		t.Parallel()
		w := newBaseWorld(t)
		w.history.add(stray, []string{"README.md"}, revA)
		w.repo.commit(stray, nil)
		// A landing in flight whose candidate main does not hold.
		if err := w.checkMain(stray, "run-2", policy.FollowProof{Candidate: cand1, Run: "run-1"}); err != nil {
			t.Fatalf("a main with an ordinary change was refused: %v", err)
		}
		w.release(cand2, merge2, stray, stray, app, nil)
		base, err := w.follow(merge2, cand2, "run-2")
		wantCode(t, err, core.CodeApprovalRequired, "following a merge made on a stray commit")
		if base.Stopped == nil || !strings.Contains(base.Stopped.Reason, "neither made nor followed") {
			t.Fatalf("the stop does not say the commit is not the factory's: %+v", base.Stopped)
		}
	})

	t.Run("without the landings the stop names a lagging follow", func(t *testing.T) {
		t.Parallel()
		w := newBaseWorld(t)
		w.release(cand1, merge1, revA, revA, app, nil)
		w.release(cand2, merge2, merge1, merge1, app, nil)
		base, err := w.follow(merge2, cand2, "run-2")
		wantCode(t, err, core.CodeApprovalRequired, "following a merge made on an unfollowed merge")
		if base.Stopped == nil || !strings.Contains(base.Stopped.Reason, "follow of an earlier release had not happened yet") {
			t.Fatalf("the stop does not name the lagging follow: %+v", base.Stopped)
		}
	})
}

// Accept takes no commit that is not part of main as it stands now,
// whatever the record lists, and judges the paths only the Owner changes by
// the governance's own list of the trusted control surface.
func TestABaseOffMainOrOnTheTrustedSurfaceIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	forge := func(t *testing.T, w *baseWorld, commit core.CommitSHA) {
		t.Helper()
		baseline, _ := w.baselines.Current(ctx)
		putRecord(t, w, policy.BuildBase{Revision: revA, Digest: baseline.Digest, Commit: commit, Chain: []core.CommitSHA{commit}})
	}

	t.Run("a listed commit that is not part of main", func(t *testing.T) {
		t.Parallel()
		w := newBaseWorld(t)
		w.history.add(other, []string{"examples/hello-app/main.go"}, revA)
		w.repo.commit(other, nil)
		forge(t, w, other)
		_, err := w.bases.Accept(ctx, other)
		if !core.HasCode(err, core.CodePolicyHold) || !strings.Contains(err.Error(), "not part of main") {
			t.Fatalf("a commit off main is accepted to build on: %v", err)
		}
		w.history.main = other
		if got, err := w.bases.Accept(ctx, other); err != nil || got != other {
			t.Fatalf("the same commit once on main is refused: %v", err)
		}
	})

	for _, path := range []string{"go.mod", "internal/supervisor/release/release.go", "cmd/edge/main.go", "internal/supplychain/verify.go"} {
		t.Run("a listed commit that changes "+path, func(t *testing.T) {
			t.Parallel()
			w := newBaseWorld(t)
			keeper := w.keeperWithRule(t, release.IsProtected)
			w.history.add(other, []string{path}, revA)
			w.history.main = other
			w.repo.commit(other, nil)
			forge(t, w, other)
			_, err := keeper.Accept(ctx, other)
			if !core.HasCode(err, core.CodePolicyHold) || !strings.Contains(err.Error(), path) {
				t.Fatalf("a commit that changes %s is accepted to build on: %v", path, err)
			}
		})
	}

	t.Run("a listed commit whose foundation files differ from what was approved", func(t *testing.T) {
		t.Parallel()
		w := newBaseWorld(t)
		// GitHub lists an ordinary path only, yet the foundation files at
		// the commit are not the approved ones.
		w.history.add(other, []string{"examples/hello-app/main.go"}, revA)
		w.history.main = other
		w.repo.commit(other, func(files map[string]string) { files[policy.PathAgents] += "\n- Agents may merge.\n" })
		forge(t, w, other)
		_, err := w.bases.Accept(ctx, other)
		if !core.HasCode(err, core.CodePolicyHold) || !strings.Contains(err.Error(), "not the content the Owner approved") {
			t.Fatalf("a commit with other foundation files is accepted to build on: %v", err)
		}
	})
}

// keeperWithRule builds a keeper over the world's store that judges the
// paths only the Owner changes by rule.
func (w *baseWorld) keeperWithRule(t *testing.T, rule func(string) bool) *policy.BuildBases {
	t.Helper()
	keeper, err := policy.NewBuildBases(policy.BuildBasesConfig{
		State: w.state, Source: w.repo, Baselines: w.baselines, Commits: w.history, Parents: w.history, Main: w.history,
		OwnerOnly: rule, Audit: w.audit, FactoryID: factory, Clock: w.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return keeper
}
