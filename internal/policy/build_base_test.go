package policy_test

import (
	"context"
	"encoding/json"
	"path"
	"strings"
	"sync"
	"testing"

	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Commits of the build-base tests: candidates an agent wrote and the merges
// the supervisor made of them on main, one after the other.
const (
	cand1  core.CommitSHA = "1111111111111111111111111111111111111111"
	cand2  core.CommitSHA = "2222222222222222222222222222222222222222"
	cand3  core.CommitSHA = "3333333333333333333333333333333333333333"
	merge1 core.CommitSHA = "4444444444444444444444444444444444444444"
	merge2 core.CommitSHA = "5555555555555555555555555555555555555555"
	merge3 core.CommitSHA = "6666666666666666666666666666666666666666"
	stray  core.CommitSHA = "7777777777777777777777777777777777777777"
)

// history is a commit graph for the comparer: which commits each one
// contains and which paths it changes on its own.
type history struct {
	mu       sync.Mutex
	contains map[core.CommitSHA][]core.CommitSHA
	touches  map[core.CommitSHA][]string
	down     bool
	calls    int
}

func newHistory() *history {
	return &history{contains: map[core.CommitSHA][]core.CommitSHA{}, touches: map[core.CommitSHA][]string{}}
}

// add records commit with its parents and the paths it changes.
func (h *history) add(commit core.CommitSHA, touches []string, parents ...core.CommitSHA) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.contains[commit] = parents
	h.touches[commit] = touches
}

// has reports whether base contains head.
func (h *history) has(base, head core.CommitSHA) bool {
	if base == head {
		return true
	}
	for _, parent := range h.contains[base] {
		if h.has(parent, head) {
			return true
		}
	}
	return false
}

// paths lists what head changes beyond base: every commit head contains
// that base does not.
func (h *history) paths(base, head core.CommitSHA, seen map[core.CommitSHA]bool) []string {
	if seen[head] || h.has(base, head) {
		return nil
	}
	seen[head] = true
	out := append([]string(nil), h.touches[head]...)
	for _, parent := range h.contains[head] {
		out = append(out, h.paths(base, parent, seen)...)
	}
	return out
}

func (h *history) ChangedPaths(_ context.Context, base, head core.CommitSHA) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	if h.down {
		return nil, core.NewError(core.CodeUnavailable, "GitHub cannot be reached")
	}
	return h.paths(base, head, map[core.CommitSHA]bool{}), nil
}

// ownerOnly is the rule of the paths only the Owner merges, as the
// supervisor passes it.
func ownerOnly(p string) bool {
	p = strings.ToLower(p)
	return strings.HasPrefix(p, ".github/") || strings.HasPrefix(p, "docs/adr/") || strings.HasPrefix(p, ".sisf/") ||
		path.Base(p) == p && (p == "agents.md" || p == "review.md" || p == "decisions.md" || p == "features.md")
}

// baseWorld is an approved factory with the commit to build on kept over
// its store.
type baseWorld struct {
	*world
	history *history
	bases   *policy.BuildBases
}

func newBaseWorld(t *testing.T) *baseWorld {
	t.Helper()
	w := &baseWorld{world: approved(t), history: newHistory()}
	w.history.add(revA, nil)
	var err error
	w.bases, err = policy.NewBuildBases(policy.BuildBasesConfig{
		State: w.state, Source: w.repo, Baselines: w.baselines, Commits: w.history, OwnerOnly: ownerOnly,
		Audit: w.audit, FactoryID: factory, Clock: w.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// release puts on main the merge of a candidate built on base that changes
// paths, with the foundation files of revA changed by edit.
func (w *baseWorld) release(candidate, merge, base core.CommitSHA, main core.CommitSHA, paths []string, edit func(map[string]string)) {
	w.history.add(candidate, paths, base)
	w.history.add(merge, nil, main, candidate)
	w.repo.commit(merge, edit)
	w.repo.commit(candidate, edit)
}

func (w *baseWorld) follow(merge, candidate core.CommitSHA, run core.RunID) (policy.BuildBase, error) {
	return w.bases.Follow(context.Background(), policy.SupervisorActor(),
		policy.FollowProof{Merge: merge, Candidate: candidate, Run: run, Unit: "hello-app"})
}

func (w *baseWorld) stored(t *testing.T) (policy.BuildBase, bool) {
	t.Helper()
	var base policy.BuildBase
	var found bool
	err := w.state.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		base, found, err = policy.ReadBuildBase(ctx, tx)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return base, found
}

func TestTheSupervisorFollowsThreeReleasesInARowWithoutTheOwner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newBaseWorld(t)
	app := []string{"examples/hello-app/main.go"}
	w.release(cand1, merge1, revA, revA, app, nil)
	w.release(cand2, merge2, merge1, merge1, app, nil)
	w.release(cand3, merge3, merge2, merge2, app, nil)

	for i, step := range []struct{ merge, candidate core.CommitSHA }{{merge1, cand1}, {merge2, cand2}, {merge3, cand3}} {
		base, err := w.follow(step.merge, step.candidate, core.RunID("run-"+string(rune('1'+i))))
		if err != nil {
			t.Fatalf("release %d: %v", i+1, err)
		}
		if base.Commit != step.merge || base.Revision != revA || base.Stopped != nil || len(base.Chain) != i+1 {
			t.Fatalf("after release %d the commit to build on is %+v", i+1, base)
		}
		if got := base.BaseFor(revA); got != step.merge {
			t.Fatalf("a run of revA is pinned to %s after release %d, want %s", got, i+1, step.merge)
		}
		if err := w.bases.Accept(ctx, step.merge); err != nil {
			t.Fatalf("the followed merge %s is not accepted to build on: %v", step.merge, err)
		}
	}
	// A run pinned to an earlier merge of the chain is still judged on it.
	for _, commit := range []core.CommitSHA{revA, merge1, merge2} {
		if err := w.bases.Accept(ctx, commit); err != nil {
			t.Fatalf("%s, coded on before the latest merge, is no longer accepted: %v", commit, err)
		}
	}
	// The policy in force and its approvals never moved.
	if snapshot := w.inForce(t); snapshot.Revision != revA {
		t.Fatalf("following main changed the policy in force to %s", snapshot.Revision)
	}
	if got := len(w.audit.Find(policy.ActionFollowMain, core.OutcomeExecuted)); got != 3 {
		t.Fatalf("%d follows were audited as executed, want 3", got)
	}
	// Following a merge again, or one the commit to build on already
	// contains, changes nothing.
	again, err := w.follow(merge2, cand2, "run-2")
	if err != nil || again.Commit != merge3 || len(again.Chain) != 3 {
		t.Fatalf("following an older merge again = %+v, %v", again, err)
	}
}

func TestAChangeToFoundationContentStopsTheFactoryUntilTheOwnerApproves(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newBaseWorld(t)
	w.release(cand1, merge1, revA, revA, []string{"examples/hello-app/main.go"}, nil)
	if _, err := w.follow(merge1, cand1, "run-1"); err != nil {
		t.Fatal(err)
	}
	// Someone changes a decision rule on main, outside the factory; the
	// next release's merge carries it.
	changeRule := func(files map[string]string) { files[policy.PathDecisions] += "\n- A new rule.\n" }
	w.history.add(revB, []string{policy.PathDecisions}, merge1)
	w.repo.commit(revB, changeRule)
	w.release(cand2, merge2, merge1, revB, []string{"examples/hello-app/main.go"}, changeRule)

	base, err := w.follow(merge2, cand2, "run-2")
	wantCode(t, err, core.CodeApprovalRequired, "following a merge that changes a foundation file")
	if base.Stopped == nil || base.Stopped.Merge != merge2 || base.Commit != merge1 || !strings.Contains(base.Stopped.Reason, policy.PathDecisions) {
		t.Fatalf("the refused follow left %+v", base)
	}
	if stop := base.StopFor(revA); stop == nil {
		t.Fatal("runs pinned to the policy in force are not held by the stop")
	}
	if err := w.bases.Accept(ctx, merge2); !core.HasCode(err, core.CodePolicyHold) {
		t.Fatalf("the merge that was not followed is accepted to build on: %v", err)
	}
	// Nothing else moves it while stopped, not even a clean merge.
	w.release(cand3, merge3, merge2, merge2, []string{"examples/hello-app/main.go"}, changeRule)
	_, err = w.follow(merge3, cand3, "run-3")
	wantCode(t, err, core.CodeApprovalRequired, "following while stopped")
	if stored, _ := w.stored(t); stored.Commit != merge1 || stored.Stopped == nil {
		t.Fatalf("a follow while stopped moved the record to %+v", stored)
	}

	// The Owner approves the new content at merge2; the stale record no
	// longer holds anything and following starts again from there.
	submission := w.requestChange(t, "apr-rule", merge2)
	if _, err := w.approvals.Submit(ctx, w.owner(), submission); err != nil {
		t.Fatal(err)
	}
	if _, err := w.baselines.Approve(ctx, w.owner(), merge2, "apr-rule"); err != nil {
		t.Fatal(err)
	}
	if err := policy.ClearStaleBuildBase(ctx, w.state, merge2); err != nil {
		t.Fatal(err)
	}
	if _, found := w.stored(t); found {
		t.Fatal("the stop is still stored after the Owner approved a revision that contains the merge")
	}
	base, err = w.follow(merge3, cand3, "run-3")
	if err != nil || base.Revision != merge2 || base.Commit != merge3 || base.Stopped != nil {
		t.Fatalf("following after the Owner's approval = %+v, %v", base, err)
	}
}

func TestOnlyTheSupervisorMovesTheCommitToBuildOn(t *testing.T) {
	t.Parallel()
	w := newBaseWorld(t)
	w.release(cand1, merge1, revA, revA, []string{"examples/hello-app/main.go"}, nil)
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
	}
	if _, found := w.stored(t); found {
		t.Fatal("a refused follow wrote the commit to build on")
	}
	if got := len(w.audit.Find(policy.ActionFollowMain, core.OutcomeDenied)); got != len(impostors) {
		t.Fatalf("%d refused follows were audited, want %d", got, len(impostors))
	}
}

func TestATamperedProofOfUnchangedContentIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	app := []string{"examples/hello-app/main.go"}

	t.Run("the merge does not contain the candidate", func(t *testing.T) {
		t.Parallel()
		w := newBaseWorld(t)
		w.release(cand1, merge1, revA, revA, app, nil)
		w.history.add(cand2, app, revA)
		w.repo.commit(cand2, nil)
		base, err := w.follow(merge1, cand2, "run-2")
		wantCode(t, err, core.CodeApprovalRequired, "a proof naming a candidate the merge lacks")
		if base.Stopped == nil || base.Commit != revA {
			t.Fatalf("the refused proof left %+v", base)
		}
	})
	t.Run("the merge does not contain the commit built on", func(t *testing.T) {
		t.Parallel()
		w := newBaseWorld(t)
		w.release(cand1, merge1, revA, revA, app, nil)
		if _, err := w.follow(merge1, cand1, "run-1"); err != nil {
			t.Fatal(err)
		}
		// A branch that left main before merge1, with the same files.
		w.release(cand2, merge2, revA, revA, app, nil)
		_, err := w.follow(merge2, cand2, "run-2")
		wantCode(t, err, core.CodeApprovalRequired, "a merge beside the commit built on")
	})
	t.Run("the merge changes a path only the Owner merges", func(t *testing.T) {
		t.Parallel()
		w := newBaseWorld(t)
		w.release(cand1, merge1, revA, revA, []string{".github/workflows/release.yml"}, nil)
		base, err := w.follow(merge1, cand1, "run-1")
		wantCode(t, err, core.CodeApprovalRequired, "a merge that changes CI")
		if base.Stopped == nil || !strings.Contains(base.Stopped.Reason, ".github/workflows/release.yml") {
			t.Fatalf("the stop does not name the Owner-only path: %+v", base.Stopped)
		}
	})
	t.Run("a commit with the approved files that the supervisor never followed", func(t *testing.T) {
		t.Parallel()
		w := newBaseWorld(t)
		w.history.add(stray, []string{"examples/hello-app/backdoor.go"}, revA)
		w.repo.commit(stray, nil)
		if err := w.bases.Accept(ctx, stray); !core.HasCode(err, core.CodePolicyHold) {
			t.Fatalf("a commit an agent names is accepted to build on: %v", err)
		}
	})
	t.Run("a stored record that lists a commit with other content", func(t *testing.T) {
		t.Parallel()
		w := newBaseWorld(t)
		w.history.add(stray, []string{policy.PathAgents}, revA)
		w.repo.commit(stray, func(files map[string]string) { files[policy.PathAgents] += "\n- Agents may merge.\n" })
		baseline, _ := w.baselines.Current(ctx)
		forged := policy.BuildBase{Revision: revA, Digest: baseline.Digest, Commit: stray, Chain: []core.CommitSHA{stray}}
		raw, _ := json.Marshal(forged)
		if err := w.state.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			_, err := tx.PutKV(ctx, state.KVEntry{Namespace: policy.BaseNamespace, Key: "build-base", Value: raw})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := w.bases.Accept(ctx, stray); !core.HasCode(err, core.CodePolicyHold) {
			t.Fatalf("a forged record makes a commit with other foundation files acceptable: %v", err)
		}
	})
	t.Run("a stored record that cannot be read", func(t *testing.T) {
		t.Parallel()
		w := newBaseWorld(t)
		if err := w.state.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			_, err := tx.PutKV(ctx, state.KVEntry{Namespace: policy.BaseNamespace, Key: "build-base", Value: []byte("{")})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		wantCode(t, w.bases.Accept(ctx, merge1), core.CodeTampered, "accepting with an unreadable record")
		_, err := w.follow(merge1, cand1, "run-1")
		wantCode(t, err, core.CodeTampered, "following with an unreadable record")
	})
}

func TestAGitHubThatCannotAnswerDecidesNothing(t *testing.T) {
	t.Parallel()
	w := newBaseWorld(t)
	w.release(cand1, merge1, revA, revA, []string{"examples/hello-app/main.go"}, nil)
	w.history.down = true
	_, err := w.follow(merge1, cand1, "run-1")
	wantCode(t, err, core.CodeUnavailable, "following while GitHub is out")
	if _, found := w.stored(t); found {
		t.Fatal("an outage stopped the factory")
	}
	w.history.down = false
	if base, err := w.follow(merge1, cand1, "run-1"); err != nil || base.Commit != merge1 {
		t.Fatalf("following once GitHub is back = %+v, %v", base, err)
	}
}

func TestTheCommitToBuildOnIsNotKeptWithoutItsParts(t *testing.T) {
	t.Parallel()
	w := newBaseWorld(t)
	full := policy.BuildBasesConfig{State: w.state, Source: w.repo, Baselines: w.baselines, Commits: w.history,
		OwnerOnly: ownerOnly, Audit: w.audit, FactoryID: factory}
	cases := map[string]func(*policy.BuildBasesConfig){
		"no state":      func(c *policy.BuildBasesConfig) { c.State = nil },
		"no source":     func(c *policy.BuildBasesConfig) { c.Source = nil },
		"no baseline":   func(c *policy.BuildBasesConfig) { c.Baselines = nil },
		"no comparer":   func(c *policy.BuildBasesConfig) { c.Commits = nil },
		"no owner rule": func(c *policy.BuildBasesConfig) { c.OwnerOnly = nil },
		"no audit log":  func(c *policy.BuildBasesConfig) { c.Audit = nil },
		"no factory id": func(c *policy.BuildBasesConfig) { c.FactoryID = "" },
	}
	for name, mutate := range cases {
		cfg := full
		mutate(&cfg)
		if _, err := policy.NewBuildBases(cfg); err == nil {
			t.Errorf("%s: built", name)
		}
	}
}
