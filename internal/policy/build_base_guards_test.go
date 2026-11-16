package policy_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// Further commits of the guard tests.
const (
	root  core.CommitSHA = "8888888888888888888888888888888888888888"
	other core.CommitSHA = "9999999999999999999999999999999999999999"
)

// keeperOver builds a keeper of the commit to build on that writes through
// store, as a process of the store's zone would.
func (w *baseWorld) keeperOver(t *testing.T, store state.Store) *policy.BuildBases {
	t.Helper()
	keeper, err := policy.NewBuildBases(policy.BuildBasesConfig{
		State: store, Source: w.repo, Baselines: w.baselines, Commits: w.history, Parents: w.history, OwnerOnly: ownerOnly,
		Audit: w.audit, FactoryID: factory, Clock: w.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return keeper
}

// The boundary is the database role, not the actor a caller names: a
// process of the agent zone that calls Follow as the supervisor still
// cannot write the record, while the control zone's store can.
func TestOnlyTheSupervisorMovesTheCommitToBuildOn(t *testing.T) {
	t.Parallel()
	app := []string{"examples/hello-app/main.go"}
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		w := newBaseWorld(t)
		w.release(cand1, merge1, revA, revA, app, nil)
		w.history.add(stray, []string{policy.PathDecisions}, revA)

		agent := w.keeperOver(t, db.Open(t, core.ZoneAgent))
		_, err := agent.Follow(ctx, policy.SupervisorActor(), policy.FollowProof{Merge: merge1, Candidate: cand1, Run: "run-1", Unit: "hello-app"})
		wantCode(t, err, core.CodePermissionDenied, "the agent zone's store moving the commit to build on")
		err = agent.CheckMain(ctx, policy.SupervisorActor(), policy.MainCheck{Head: stray, Run: "run-1", Unit: "hello-app"})
		wantCode(t, err, core.CodePermissionDenied, "the agent zone's store recording a stop")

		control := db.Open(t, core.ZoneControl)
		var found bool
		if err := control.View(ctx, func(ctx context.Context, tx state.Tx) error {
			_, found, err = policy.ReadBuildBase(ctx, tx)
			return err
		}); err != nil || found {
			t.Fatalf("the agent zone wrote the commit to build on: found %v, %v", found, err)
		}
		base, err := w.keeperOver(t, control).Follow(ctx, policy.SupervisorActor(),
			policy.FollowProof{Merge: merge1, Candidate: cand1, Run: "run-1", Unit: "hello-app"})
		if err != nil || base.Commit != merge1 {
			t.Fatalf("the control zone could not follow the release: %+v, %v", base, err)
		}
	})

	t.Run("an actor other than the supervisor", func(t *testing.T) {
		t.Parallel()
		w := newBaseWorld(t)
		w.release(cand1, merge1, revA, revA, app, nil)
		impostors := map[string]core.Actor{
			"an agent":                 {ID: policy.SupervisorActorID, Kind: core.ActorAgent},
			"the Owner":                w.owner(),
			"another service":          {ID: "service:publisher", Kind: core.ActorService},
			"a service with no name":   {Kind: core.ActorService},
			"an agent of the run name": {ID: "agent:claude/coder", Kind: core.ActorAgent, Roles: []core.Role{core.RoleOwner}},
		}
		for name, actor := range impostors {
			_, err := w.bases.Follow(context.Background(), actor, policy.FollowProof{Merge: merge1, Candidate: cand1, Run: "run-1"})
			wantCode(t, err, core.CodePermissionDenied, name+" moving the commit to build on")
			err = w.bases.CheckMain(context.Background(), actor, policy.MainCheck{Head: merge1, Run: "run-1"})
			wantCode(t, err, core.CodePermissionDenied, name+" checking main")
		}
		if _, found := w.stored(t); found {
			t.Fatal("a refused follow wrote the commit to build on")
		}
		if got := len(w.audit.Find(policy.ActionFollowMain, core.OutcomeDenied)); got != 2*len(impostors) {
			t.Fatalf("%d refused attempts were audited, want %d", got, 2*len(impostors))
		}
	})
}

// The factory follows only what it judged: a commit pushed to main outside
// the factory, or anything a merge adds beyond the candidate QA judged,
// stops it.
func TestTheFactoryFollowsOnlyTheCandidateItJudged(t *testing.T) {
	t.Parallel()
	app := []string{"examples/hello-app/main.go"}

	t.Run("a commit pushed to main before the merge", func(t *testing.T) {
		t.Parallel()
		w := newBaseWorld(t)
		backdoor := []string{"examples/hello-app/backdoor.go"}
		w.history.add(stray, backdoor, revA)
		w.repo.commit(stray, nil)
		w.release(cand1, merge1, revA, stray, app, nil)
		base, err := w.follow(merge1, cand1, "run-1")
		wantCode(t, err, core.CodeApprovalRequired, "following a merge made on a commit pushed outside the factory")
		if base.Stopped == nil || base.Commit != revA || !strings.Contains(base.Stopped.Reason, string(stray)) {
			t.Fatalf("the merge over a pushed commit left %+v", base)
		}
	})
	t.Run("a merge that adds more than the candidate holds", func(t *testing.T) {
		t.Parallel()
		w := newBaseWorld(t)
		w.release(cand1, merge1, revA, revA, app, nil)
		// The merge commit itself carries a change of go.mod that the
		// candidate QA judged does not.
		w.history.add(merge1, []string{"go.mod"}, revA, cand1)
		w.repo.mu.Lock()
		w.repo.commits[merge1]["go.mod"] = "module evil\n"
		w.repo.mu.Unlock()
		base, err := w.follow(merge1, cand1, "run-1")
		wantCode(t, err, core.CodeApprovalRequired, "following a merge that brings in a file the candidate lacks")
		if base.Stopped == nil || !strings.Contains(base.Stopped.Reason, "go.mod") {
			t.Fatalf("the stop does not name what the merge brought in: %+v", base.Stopped)
		}
	})
	t.Run("foundation files the comparison does not list", func(t *testing.T) {
		t.Parallel()
		w := newBaseWorld(t)
		// GitHub lists only the application file, yet the foundation files
		// at the merge differ: the digest alone shows it.
		w.release(cand1, merge1, revA, revA, app, func(files map[string]string) { files[policy.PathAgents] += "\n- Agents may merge.\n" })
		base, err := w.follow(merge1, cand1, "run-1")
		wantCode(t, err, core.CodeApprovalRequired, "following a merge whose foundation content differs")
		if base.Stopped == nil || !strings.Contains(base.Stopped.Reason, "not the content the Owner approved") {
			t.Fatalf("the digest did not stop the follow: %+v", base.Stopped)
		}
	})
}

// While the factory is stopped nothing moves the commit to build on, not
// even a clean merge on the commit it builds on, and no base is accepted.
func TestNothingIsFollowedOrAcceptedWhileTheFactoryIsStopped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newBaseWorld(t)
	app := []string{"examples/hello-app/main.go"}
	w.release(cand1, merge1, revA, revA, app, nil)
	if _, err := w.follow(merge1, cand1, "run-1"); err != nil {
		t.Fatal(err)
	}
	// Someone pushes a change of the decision rules to main.
	w.history.add(stray, []string{policy.PathDecisions}, merge1)
	w.repo.commit(stray, func(files map[string]string) { files[policy.PathDecisions] += "\n- A rule.\n" })
	err := w.bases.CheckMain(ctx, policy.SupervisorActor(), policy.MainCheck{Head: stray, Run: "run-2", Unit: "hello-app"})
	wantCode(t, err, core.CodeApprovalRequired, "merging into a main with changed decision rules")
	stopped, _ := w.stored(t)
	if stopped.Stopped == nil || stopped.Stopped.Merge != stray || !strings.Contains(stopped.Stopped.Reason, policy.PathDecisions) {
		t.Fatalf("the refused merge left %+v", stopped)
	}

	// A clean release built and merged on the followed commit.
	w.release(cand2, merge2, merge1, merge1, app, nil)
	_, err = w.follow(merge2, cand2, "run-2")
	wantCode(t, err, core.CodeApprovalRequired, "following while stopped")
	if after, _ := w.stored(t); after.Commit != merge1 || after.Stopped == nil {
		t.Fatalf("a follow while stopped moved the record to %+v", after)
	}
	for _, base := range []core.CommitSHA{revA, merge1} {
		if _, err := w.bases.Accept(ctx, base); !core.HasCode(err, core.CodePolicyHold) {
			t.Fatalf("%s is accepted to build on while the factory is stopped: %v", base, err)
		}
	}
	wantCode(t, w.bases.CheckMain(ctx, policy.SupervisorActor(), policy.MainCheck{Head: merge1, Run: "run-3"}),
		core.CodeApprovalRequired, "merging while stopped")
}

// A main that only moved by a change the Owner need not approve is merged
// into; following that merge is what stops the factory.
func TestAPushOfOrdinaryFilesDoesNotStopTheMergeButTheFollow(t *testing.T) {
	t.Parallel()
	w := newBaseWorld(t)
	w.history.add(stray, []string{"README.md"}, revA)
	w.repo.commit(stray, nil)
	if err := w.bases.CheckMain(context.Background(), policy.SupervisorActor(), policy.MainCheck{Head: stray, Run: "run-1"}); err != nil {
		t.Fatalf("a main with an ordinary change was refused: %v", err)
	}
	if _, found := w.stored(t); found {
		t.Fatal("an ordinary change of main stopped the factory before the merge")
	}
}

// Accept checks a listed commit again on what GitHub shows, whatever the
// record claims: it must contain the approved revision and change no path
// only the Owner may change since it.
func TestAForgedRecordCannotMakeABaseAcceptable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	forge := func(t *testing.T, w *baseWorld, commit core.CommitSHA) {
		t.Helper()
		baseline, _ := w.baselines.Current(ctx)
		putRecord(t, w, policy.BuildBase{Revision: revA, Digest: baseline.Digest, Commit: commit, Chain: []core.CommitSHA{commit}})
	}

	t.Run("a commit beside the approved revision", func(t *testing.T) {
		t.Parallel()
		w := newBaseWorld(t)
		w.history.add(root, nil)
		w.history.add(revA, []string{"README.md"}, root)
		w.history.add(other, []string{"examples/hello-app/main.go"}, root)
		w.repo.commit(other, nil)
		forge(t, w, other)
		if _, err := w.bases.Accept(ctx, other); !core.HasCode(err, core.CodePolicyHold) {
			t.Fatalf("a commit that lacks the approved revision is accepted: %v", err)
		}
	})
	t.Run("a commit that changes CI and the module", func(t *testing.T) {
		t.Parallel()
		w := newBaseWorld(t)
		w.history.add(other, []string{".github/workflows/x.yml", "go.mod"}, revA)
		w.repo.commit(other, nil)
		forge(t, w, other)
		_, err := w.bases.Accept(ctx, other)
		if !core.HasCode(err, core.CodePolicyHold) || !strings.Contains(err.Error(), ".github/workflows/x.yml") {
			t.Fatalf("a commit that changes Owner-only paths is accepted: %v", err)
		}
	})
}

// putRecord stores base as the record of the commit to build on.
func putRecord(t *testing.T, w *baseWorld, base policy.BuildBase) {
	t.Helper()
	raw := mustJSON(t, base)
	if err := w.state.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		_, err := tx.PutKV(ctx, state.KVEntry{Namespace: policy.BaseNamespace, Key: "build-base", Value: raw})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// mustJSON encodes v.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
