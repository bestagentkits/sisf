package setupactions_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/internal/actions/setupactions"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

var repo = github.RepoRef{Owner: "acme", Name: "app"}

type opener struct {
	opened []github.NewIssue
	err    error
}

func (o *opener) CreateIssue(_ context.Context, r github.RepoRef, n github.NewIssue) (github.Issue, error) {
	if o.err != nil {
		return github.Issue{}, o.err
	}
	o.opened = append(o.opened, n)
	return github.Issue{Ref: core.IssueRef{Owner: r.Owner, Repo: r.Name, Number: 42}, Title: n.Title}, nil
}

func run(t *testing.T, o *opener, dry bool) (core.ActionResult, error) {
	t.Helper()
	set, err := setupactions.Actions(repo, o)
	if err != nil {
		t.Fatal(err)
	}
	return set[0].Handle(context.Background(), core.ActionRequest{DryRun: dry, Input: json.RawMessage(`{}`)})
}

func TestTheSampleIssueNeedsARepositoryAndAClient(t *testing.T) {
	t.Parallel()
	if _, err := setupactions.Actions(github.RepoRef{}, &opener{}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("got %v", err)
	}
	if _, err := setupactions.Actions(repo, nil); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("got %v", err)
	}
}

func TestTheSampleIssueIsAnOwnerWriteWithAScope(t *testing.T) {
	t.Parallel()
	s := setupactions.Spec()
	if s.Sensitivity != core.SensitivityWrite || len(s.Scopes) == 0 || !slices.Equal(s.Roles, []core.Role{core.RoleOwner}) {
		t.Fatalf("spec %+v", s)
	}
}

func TestADryRunOpensNothing(t *testing.T) {
	t.Parallel()
	o := &opener{}
	res, err := run(t, o, true)
	if err != nil || res.Plan == nil || len(o.opened) != 0 {
		t.Fatalf("plan %v, err %v, opened %d", res.Plan, err, len(o.opened))
	}
}

func TestTheSampleIssueIsOpenedReady(t *testing.T) {
	t.Parallel()
	o := &opener{}
	res, err := run(t, o, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.opened) != 1 || o.opened[0].Title != setupactions.SampleTitle || !slices.Contains(o.opened[0].Labels, github.LabelReady) {
		t.Fatalf("opened %+v", o.opened)
	}
	var out struct {
		Issue string `json:"issue"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil || out.Issue != "acme/app#42" {
		t.Fatalf("output %s: %v", res.Output, err)
	}
}

func TestAGitHubFailureIsUnavailable(t *testing.T) {
	t.Parallel()
	if _, err := run(t, &opener{err: errors.New("502")}, false); core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("got %v", err)
	}
}
