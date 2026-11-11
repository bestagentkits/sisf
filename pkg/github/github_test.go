package github_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

func TestOnlyOwnersMembersAndCollaboratorsCountAsCollaborators(t *testing.T) {
	for _, a := range []github.AuthorAssociation{github.AssociationOwner, github.AssociationMember, github.AssociationCollaborator} {
		if !a.IsCollaborator() {
			t.Errorf("%q has write access to the repository", a)
		}
	}
	for _, a := range []github.AuthorAssociation{github.AssociationContributor, github.AssociationFirstTimeContributor,
		github.AssociationFirstTimer, github.AssociationNone, "", "owner", "ADMIN"} {
		if a.IsCollaborator() {
			t.Errorf("%q must be treated as an outsider", a)
		}
	}
}

func TestPullRequestCarriesWhereItCameFromAndWhoWroteIt(t *testing.T) {
	fork := github.PullRequest{
		Repo: github.RepoRef{Owner: "acme", Name: "app"}, Number: 12,
		HeadRepo: github.RepoRef{Owner: "stranger", Name: "app"}, IsFork: true,
		AuthorAssociation: github.AssociationNone,
	}
	if !fork.IsFork || fork.AuthorAssociation.IsCollaborator() {
		t.Fatal("a fork pull request from an outsider must be recognizable as such")
	}
	if fork.Repo.String() != "acme/app" {
		t.Fatalf("RepoRef.String() = %q", fork.Repo.String())
	}
	if fork.Repo.Issue(12) != (core.IssueRef{Owner: "acme", Repo: "app", Number: 12}) {
		t.Fatalf("RepoRef.Issue = %+v", fork.Repo.Issue(12))
	}
}

func TestFrameworkLabelsKeepTheirContractSpelling(t *testing.T) {
	status := []string{github.LabelNeedsInfo, github.LabelApproved, github.LabelRunning, github.LabelQA,
		github.LabelCanary, github.LabelBlocked, github.LabelVerified}
	want := []string{"sisf:needs-info", "sisf:approved", "sisf:running", "sisf:qa", "sisf:canary", "sisf:blocked", "sisf:verified"}
	if !slices.Equal(status, want) || !slices.Equal(github.StatusLabels(), want) {
		t.Fatalf("status labels = %v", status)
	}
	if github.LabelReady != "sisf:ready" {
		t.Fatalf("LabelReady = %q", github.LabelReady)
	}
	if slices.Contains(github.StatusLabels(), github.LabelReady) {
		t.Fatal("the intake label is applied by people and is not a status the framework owns")
	}
	kinds := []string{github.LabelKindBug, github.LabelKindImprovement, github.LabelKindFeature, github.LabelKindQuestion}
	if !slices.Equal(kinds, []string{"kind:bug", "kind:improvement", "kind:feature", "kind:question"}) {
		t.Fatalf("kind labels = %v", kinds)
	}
	if github.LabelAgentPrefix != "agent:" || github.LabelDeploymentPrefix != "deployment:" {
		t.Fatal("routing label prefixes changed")
	}
}

func TestWebhookEventValidation(t *testing.T) {
	good := github.WebhookEvent{
		DeliveryID: "72d3162e-cc78-11e3-81ab-4c9367dc0958", Type: "issues", Action: "labeled",
		InstallationID: 1, Repo: github.RepoRef{Owner: "acme", Name: "app"},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	cases := map[string]func(*github.WebhookEvent){
		"no delivery id":  func(e *github.WebhookEvent) { e.DeliveryID = "" },
		"no type":         func(e *github.WebhookEvent) { e.Type = "" },
		"no installation": func(e *github.WebhookEvent) { e.InstallationID = 0 },
		"no repository":   func(e *github.WebhookEvent) { e.Repo = github.RepoRef{} },
	}
	for name, mutate := range cases {
		e := good
		mutate(&e)
		if core.CodeOf(e.Validate()) != core.CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, e.Validate())
		}
	}
}

// clientStub proves the contract can be met with the exported types alone.
type clientStub struct{}

func (clientStub) GetIssue(_ context.Context, ref core.IssueRef) (github.Issue, error) {
	return github.Issue{Ref: ref, State: github.IssueOpen}, nil
}
func (clientStub) ListIssues(context.Context, github.RepoRef, github.ListIssuesOptions) ([]github.Issue, error) {
	return nil, nil
}
func (clientStub) CreateIssue(_ context.Context, repo github.RepoRef, n github.NewIssue) (github.Issue, error) {
	return github.Issue{Ref: repo.Issue(1), Title: n.Title}, nil
}
func (clientStub) SetIssueState(context.Context, core.IssueRef, github.IssueState) error { return nil }
func (clientStub) AddLabels(context.Context, core.IssueRef, ...string) error             { return nil }
func (clientStub) RemoveLabel(context.Context, core.IssueRef, string) error              { return nil }
func (clientStub) ListComments(context.Context, core.IssueRef, time.Time) ([]github.Comment, error) {
	return nil, nil
}
func (clientStub) CreateComment(_ context.Context, ref core.IssueRef, body string) (github.Comment, error) {
	return github.Comment{ID: 1, Issue: ref, Body: body}, nil
}
func (clientStub) UpdateComment(_ context.Context, repo github.RepoRef, id int64, body string) (github.Comment, error) {
	return github.Comment{ID: id, Body: body}, nil
}
func (clientStub) GetPullRequest(context.Context, github.RepoRef, int) (github.PullRequest, error) {
	return github.PullRequest{}, core.NewError(core.CodeNotFound, "no such pull request")
}
func (clientStub) CreatePullRequest(context.Context, github.RepoRef, github.NewPullRequest) (github.PullRequest, error) {
	return github.PullRequest{}, core.NewError(core.CodeRateLimited, "secondary rate limit")
}
func (clientStub) PutCheckRun(_ context.Context, _ github.RepoRef, c github.CheckRun) (github.CheckRun, error) {
	c.ID = 1
	return c, nil
}
func (clientStub) CreateRelease(_ context.Context, _ github.RepoRef, r github.Release) (github.Release, error) {
	return r, nil
}

func TestClientIsTheUnionOfItsNarrowRoles(t *testing.T) {
	var client github.Client = clientStub{}
	var (
		issues   github.Issues       = client
		pulls    github.PullRequests = client
		checks   github.Checks       = client
		releases github.Releases     = client
	)
	ctx := context.Background()
	repo := github.RepoRef{Owner: "acme", Name: "app"}
	comment, err := issues.CreateComment(ctx, repo.Issue(7), "progress")
	if err != nil || comment.Issue.Number != 7 {
		t.Fatalf("CreateComment = %+v, %v", comment, err)
	}
	if _, err := pulls.CreatePullRequest(ctx, repo, github.NewPullRequest{}); !core.HasCode(err, core.CodeRateLimited) {
		t.Fatalf("a provider limit must surface as RATE_LIMITED, got %v", err)
	}
	check, err := checks.PutCheckRun(ctx, repo, github.CheckRun{Name: "sisf/qa", Status: github.CheckCompleted, Conclusion: github.ConclusionFailure})
	if err != nil || check.ID == 0 {
		t.Fatalf("PutCheckRun = %+v, %v", check, err)
	}
	if _, err := releases.CreateRelease(ctx, repo, github.Release{Tag: "v0.1.0"}); err != nil {
		t.Fatal(err)
	}
}

// GitHub compares label names without regard to case, so an issue carries a
// label under any casing, and only under the whole name.
func TestHasLabelIgnoresCase(t *testing.T) {
	t.Parallel()
	issue := github.Issue{Labels: []github.Label{{Name: "SISF:Needs-Info"}, {Name: "Kind:bug"}}}
	for name, want := range map[string]bool{
		github.LabelNeedsInfo: true, "sisf:needs-info ": false, "sisf:needs": false, github.LabelKindBug: true, github.LabelReady: false,
	} {
		if got := issue.HasLabel(name); got != want {
			t.Errorf("HasLabel(%q) = %v, want %v", name, got, want)
		}
	}
}
