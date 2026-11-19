package stages

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// sampleFactory is the factory the repository's own foundation files
// declare.
const sampleFactory = core.FactoryID("acme-hello")

// foundationFiles are the repository's own foundation files, so the keeper
// of the commit to build on reads a real policy from the fake repository.
func foundationFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, path := range []string{policy.PathFactory, policy.PathAgents, policy.PathReview, policy.PathDecisions, policy.PathFeatures} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", path))
		if err != nil {
			t.Fatal(err)
		}
		files[path] = string(raw)
	}
	return files
}

// unspentApprovals is an approval store the first baseline never reaches.
type unspentApprovals struct{ approvals.Store }

// approveFirstBaseline puts revision in force as the factory's first
// policy, as the Owner does at install.
func approveFirstBaseline(t *testing.T, store state.Store, tree Tree, revision core.CommitSHA) {
	t.Helper()
	baselines, err := policy.NewBaselines(policy.BaselinesConfig{State: store, Source: tree, Approvals: unspentApprovals{}, Audit: nopAudit{}, FactoryID: sampleFactory})
	if err != nil {
		t.Fatal(err)
	}
	owner := core.Actor{ID: "owner", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner}}
	if _, err := baselines.Approve(context.Background(), owner, revision, ""); err != nil {
		t.Fatal(err)
	}
}

// verifyRun takes the issue's record through its lifecycle to Verified for
// run, as the control zone writes it.
func verifyRun(t *testing.T, store state.Store, ref core.IssueRef, run core.RunID) {
	t.Helper()
	ctx := context.Background()
	var lease state.Lease
	if err := store.Update(ctx, func(ctx context.Context, tx state.Tx) (err error) {
		lease, err = tx.AcquireLease(ctx, state.IssueLeaseResource(ref), "supervisor", time.Hour)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var record core.StateRecord
	for _, event := range []core.Event{core.EventIssueReceived, core.EventInfoSufficient, core.EventScopePermitted, core.EventClaimed,
		core.EventDevelopmentStarted, core.EventCandidateSubmitted, core.EventQAPassed, core.EventRolloutStarted,
		core.EventRolloutHealthy, core.EventPostDeployVerified} {
		to, err := statemachine.Next(record.State, event)
		if err != nil {
			t.Fatal(err)
		}
		tr := core.Transition{IssueID: ref, From: record.State, To: to, Event: event, ExpectedVersion: record.Version,
			IdempotencyKey: "test/" + string(run) + "/" + string(event), ActorID: "supervisor", RunID: record.RunID, Attempt: record.Attempt,
			CandidateDigest: record.CandidateDigest, ReleaseDigest: record.ReleaseDigest}
		if event == core.EventClaimed || statemachine.RequiresLease(record.State) {
			tr.LeaseOwner, tr.FencingToken = lease.Owner, lease.Token
		}
		switch event {
		case core.EventClaimed:
			tr.RunID, tr.Attempt = run, 1
		case core.EventCandidateSubmitted:
			tr.CandidateDigest = landingDigest
		case core.EventRolloutStarted:
			tr.ReleaseDigest = core.ReleaseDigest(landingDigest)
		}
		err = store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			record, err = tx.Transition(ctx, tr)
			return err
		})
		if err != nil {
			t.Fatalf("%s: %v", event, err)
		}
	}
}

// nextCandidate adds a verified candidate of run to the world, coded on
// base, writing files, and returns the request that lands it.
func (w *landingWorld) nextCandidate(t *testing.T, run core.RunID, base core.CommitSHA, files map[string][]byte) release.LandRequest {
	t.Helper()
	ctx := context.Background()
	number, err := w.fake.OpenIssue(landingRepo, w.fake.Person("maintainer", "MEMBER"), "Say more", "body")
	if err != nil {
		t.Fatal(err)
	}
	issue := core.IssueRef{Owner: "acme", Repo: "app", Number: number}
	branch := Branch("f", run, 1)
	commit, err := w.client.CommitToBranch(ctx, w.prs.Repo, github.BranchCommit{Branch: branch, Base: base, Message: "candidate",
		Author: "service:builder", Files: files})
	if err != nil {
		t.Fatal(err)
	}
	var changed []string
	for p := range files {
		changed = append(changed, p)
	}
	candidate := qa.Candidate{IssueID: issue, RunID: run, CommitSHA: commit, Digest: landingDigest, Attempt: 1, ChangedPaths: changed}
	receipt := core.QAReceipt{Score: 92, PolicySHA: w.inForce, BaselineSHA: base, CandidateSHA: commit}
	err = w.prs.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		if err := put(ctx, tx, QAResultKey(run, 1), QAResult{Candidate: candidate, Receipt: receipt, Verdict: VerdictPassed}); err != nil {
			return err
		}
		if err := handoff.PutDoc(ctx, tx, handoff.BuildNamespace, handoff.CandidateKey(run, 1), handoff.CandidateDoc{Candidate: candidate, Branch: branch}); err != nil {
			return err
		}
		return handoff.PutDoc(ctx, tx, handoff.BuildNamespace, handoff.BuildKey(commit), handoff.BuildRecord{Commit: commit, Digest: landingDigest, Unit: "app"})
	})
	if err != nil {
		t.Fatal(err)
	}
	return release.LandRequest{IssueID: issue, RunID: run, Release: core.ReleaseDigest(landingDigest), Candidate: candidate, Receipt: receipt}
}

// A release is merged and verified, and its follow has not happened, as
// after an outage, when the next release is about to merge. The check of
// main finds it among the verified releases in the store and the keeper
// follows its merge before the next one is made on it; the factory keeps
// building, and the next merge is followed in turn.
func TestAVerifiedMergeWhoseFollowLagsIsFollowedBeforeTheNextMerge(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		store := db.OpenFactory(t, sampleFactory, core.ZoneControl)
		base := foundationFiles(t)
		base["examples/hello-app/main.go"] = "old\n"
		w := newLandingWorldOf(t, store, base, map[string][]byte{"examples/hello-app/main.go": []byte("new\n")}, []string{"examples/hello-app/main.go"})
		tree := Tree{GitHub: w.client, Repo: w.prs.Repo}
		approveFirstBaseline(t, store, tree, w.inForce)

		// The first release merges while no keeper is wired: its follow
		// lags, and its issue stays Verified.
		first, err := w.prs.Land(ctx, w.req)
		if err != nil || !first.Merged {
			t.Fatalf("the first release did not merge: %+v, %v", first, err)
		}
		verifyRun(t, store, w.req.IssueID, w.req.RunID)

		cfg := Config{State: store, FactoryID: sampleFactory, GitHub: w.client, Repo: w.prs.Repo, BaseBranch: "main", Clock: core.SystemClock{}}
		keeper, err := wireCommitToBuildOn(cfg, &release.Config{Audit: nopAudit{}, Commits: tree}, tree, w.prs)
		if err != nil || keeper == nil {
			t.Fatalf("no keeper: %v", err)
		}
		next := w.nextCandidate(t, "run-2", first.Commit, map[string][]byte{"examples/hello-app/greeting.txt": []byte("hi\n")})
		second, err := w.prs.Land(ctx, next)
		if err != nil || !second.Merged {
			t.Fatalf("the next release did not merge on the first: %+v, %v", second, err)
		}

		var record policy.BuildBase
		if err := store.View(ctx, func(ctx context.Context, tx state.Tx) (err error) {
			record, _, err = policy.ReadBuildBase(ctx, tx)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if record.Stopped != nil || record.Commit != first.Commit || !slices.Equal(record.Chain, []core.CommitSHA{first.Commit}) || record.Run != w.req.RunID {
			t.Fatalf("the lagging merge %s was not followed before the next merge: %+v", first.Commit, record)
		}
		if _, err := keeper.Follow(ctx, policy.SupervisorActor(), policy.FollowProof{Merge: second.Commit, Candidate: next.Candidate.CommitSHA, Run: next.RunID, Unit: "app"}); err != nil {
			t.Fatalf("the next merge, made on the followed one, was not followed: %v", err)
		}
	})
}
