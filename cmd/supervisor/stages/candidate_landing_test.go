package stages

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/internal/testkit/fakegithub"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

const (
	landingRepo   = "acme/app"
	landingDigest = core.Digest("sha256:1111111111111111111111111111111111111111111111111111111111111111")
	pinnedPolicy  = core.CommitSHA("2222222222222222222222222222222222222222")
)

// landingWorld is a repository with a candidate branch off main, the
// supervisor's records of the candidate QA passed, and the stage that lands
// it.
type landingWorld struct {
	prs     *CandidatePRs
	data    *fakegithub.GitData
	client  *github.RESTClient
	req     release.LandRequest
	branch  string
	inForce core.CommitSHA
}

func newLandingWorld(t *testing.T, store state.Store, changed ...string) *landingWorld {
	t.Helper()
	ctx := context.Background()
	fake := fakegithub.New(fakegithub.WithToken("token"))
	if err := fake.AddRepo(landingRepo); err != nil {
		t.Fatal(err)
	}
	data := fakegithub.NewGitData(fake)
	srv := httptest.NewServer(data)
	t.Cleanup(srv.Close)
	client, err := github.NewRESTClient(github.ClientConfig{BaseURL: srv.URL, Token: github.StaticToken(core.NewSecretValue([]byte("token")))})
	if err != nil {
		t.Fatal(err)
	}
	base, err := fake.Commit(landingRepo, map[string]string{"app/main.go": "old\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := data.SetBranch(landingRepo, "main", base); err != nil {
		t.Fatal(err)
	}
	repo := github.RepoRef{Owner: "acme", Name: "app"}
	number, err := fake.OpenIssue(landingRepo, fake.Person("maintainer", "MEMBER"), "Greet the world", "body")
	if err != nil {
		t.Fatal(err)
	}
	issue := core.IssueRef{Owner: "acme", Repo: "app", Number: number}
	run := core.RunID("run-1")
	branch := Branch("f", run, 1)
	commit, err := client.CommitToBranch(ctx, repo, github.BranchCommit{Branch: branch, Base: core.CommitSHA(base), Message: "candidate",
		Author: "service:builder", Files: map[string][]byte{"app/main.go": []byte("new\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) == 0 {
		changed = []string{"app/main.go"}
	}
	candidate := qa.Candidate{IssueID: issue, RunID: run, CommitSHA: commit, Digest: landingDigest, Attempt: 1, ChangedPaths: changed}
	receipt := core.QAReceipt{Score: 92, PolicySHA: pinnedPolicy}
	err = store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		if err := put(ctx, tx, QAResultKey(run, 1), QAResult{Candidate: candidate, Receipt: receipt, Verdict: VerdictPassed}); err != nil {
			return err
		}
		if err := handoff.PutDoc(ctx, tx, handoff.BuildNamespace, handoff.CandidateKey(run, 1), handoff.CandidateDoc{Candidate: candidate, Branch: branch}); err != nil {
			return err
		}
		return handoff.PutDoc(ctx, tx, handoff.BuildNamespace, handoff.BuildKey(commit), handoff.BuildRecord{Commit: commit, Digest: landingDigest})
	})
	if err != nil {
		t.Fatal(err)
	}
	merger, err := github.NewMerger(client)
	if err != nil {
		t.Fatal(err)
	}
	w := &landingWorld{data: data, client: client, branch: branch, inForce: pinnedPolicy,
		req: release.LandRequest{IssueID: issue, RunID: run, Release: core.ReleaseDigest(landingDigest), Candidate: candidate, Receipt: receipt}}
	w.prs = &CandidatePRs{Repo: repo, Base: "main", State: store, GitHub: client, Merger: merger, Clock: core.SystemClock{},
		Policy: func(context.Context) (core.PolicyRefs, error) { return core.PolicyRefs{PolicySHA: w.inForce}, nil }}
	return w
}

func (w *landingWorld) mainHasTheChange() bool {
	_, files, _ := w.data.Branch(landingRepo, "main")
	return files["app/main.go"] == "new\n"
}

// The supervisor merges a verified candidate only while everything it was
// judged and released as still holds; otherwise the pull request is left
// for the Owner with the reason, and nothing reaches main.
func TestAVerifiedCandidateIsMergedOnlyWhileItIsWhatWasJudgedAndReleased(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		for name, c := range map[string]struct {
			changed []string
			bend    func(t *testing.T, w *landingWorld)
			reason  string // "" means merged
		}{
			"as judged and released": {},
			"a foundation file":      {changed: []string{"app/main.go", "AGENTS.md"}, reason: "AGENTS.md, which only the Owner merges"},
			"an architecture record": {changed: []string{"docs/adr/0099-x.md"}, reason: "docs/adr/0099-x.md"},
			"the policy changed": {bend: func(_ *testing.T, w *landingWorld) {
				w.inForce = "3333333333333333333333333333333333333333"
			}, reason: "policy in force changed"},
			"another build was released": {bend: func(_ *testing.T, w *landingWorld) {
				w.req.Release = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
			}, reason: "not the build"},
			"the branch moved": {bend: func(t *testing.T, w *landingWorld) {
				if _, err := w.prs.ensure(context.Background(), w.req.IssueID, w.req.RunID, 1); err != nil {
					t.Fatal(err)
				}
				if _, err := w.client.CommitToBranch(context.Background(), w.prs.Repo, github.BranchCommit{Branch: w.branch,
					Base: w.req.Candidate.CommitSHA, Message: "more", Author: "someone", Files: map[string][]byte{"app/x.go": []byte("x\n")}}); err != nil {
					t.Fatal(err)
				}
			}, reason: "branch moved"},
		} {
			t.Run(name, func(t *testing.T) {
				factory := core.FactoryID("f-" + strings.ReplaceAll(name, " ", "-"))
				w := newLandingWorld(t, db.OpenFactory(t, factory, core.ZoneControl), c.changed...)
				if c.bend != nil {
					c.bend(t, w)
				}
				landing, err := w.prs.Land(context.Background(), w.req)
				if err != nil {
					t.Fatal(err)
				}
				if c.reason == "" {
					if !landing.Merged || !w.mainHasTheChange() || w.data.Merges() != 1 {
						t.Fatalf("not merged: %+v after %d merges", landing, w.data.Merges())
					}
					return
				}
				if landing.Merged || !strings.Contains(landing.Summary, c.reason) || w.mainHasTheChange() || w.data.Merges() != 0 {
					t.Fatalf("landing %+v after %d merges, want it left for the Owner because %q", landing, w.data.Merges(), c.reason)
				}
			})
		}
	})
}

// A landing is decided once. One whose merge GitHub made before the
// supervisor recorded it, as when it restarted in between, counts as
// merged and is not merged again.
func TestALandingIsDecidedOnceAndAMadeMergeIsNotRepeated(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		w := newLandingWorld(t, db.Open(t, core.ZoneControl))
		pr, err := w.prs.ensure(ctx, w.req.IssueID, w.req.RunID, 1)
		if err != nil {
			t.Fatal(err)
		}
		pr.State = handoff.LandingPending
		if err := w.prs.put(ctx, pr); err != nil {
			t.Fatal(err)
		}
		if _, err := w.prs.Merger.Merge(ctx, w.prs.Repo, github.MergeRequest{Number: pr.Number, HeadSHA: pr.Commit, Title: "merge"}); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			landing, err := w.prs.Land(ctx, w.req)
			if err != nil || !landing.Merged {
				t.Fatalf("Land = %+v, %v", landing, err)
			}
		}
		if w.data.Merges() != 1 {
			t.Fatalf("merged %d times", w.data.Merges())
		}
		if again, err := w.prs.ensure(ctx, w.req.IssueID, w.req.RunID, 1); err != nil || again.Number != pr.Number {
			t.Fatalf("a second pull request was opened: %+v, %v", again, err)
		}
	})
}
