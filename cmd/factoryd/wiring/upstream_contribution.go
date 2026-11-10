package wiring

import (
	"context"
	"fmt"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// PullRequestOpener opens pull requests; github.RESTClient is one.
type PullRequestOpener interface {
	CreatePullRequest(ctx context.Context, repo github.RepoRef, pr github.NewPullRequest) (github.PullRequest, error)
}

// Contribution asks to offer one of the factory's branches to the upstream.
type Contribution struct {
	// Branch is the factory's branch to offer, for example a verified
	// candidate's branch.
	Branch string
	Title  string
	Body   string
}

// ContributeUpstream opens a pull request from the factory's own
// repository to the upstream one. A factory's improvements stay in its own
// state and branches: only the Owner, acting in person, can send one
// upstream, and anyone else gets APPROVAL_REQUIRED.
func (c *Control) ContributeUpstream(ctx context.Context, actor core.Actor, req Contribution) (github.PullRequest, error) {
	upstream := c.cfg.Upstream
	switch {
	case upstream.Owner == "" || upstream.Name == "":
		return github.PullRequest{}, core.NewError(core.CodeNotSupported, "the factory has no upstream repository")
	case actor.Kind != core.ActorHuman || !actor.HasRole(core.RoleOwner):
		return github.PullRequest{}, core.NewError(core.CodeApprovalRequired,
			"contributing to the upstream repository needs the Owner to trigger it")
	case !validBranch(req.Branch) || strings.TrimSpace(req.Title) == "":
		return github.PullRequest{}, core.NewError(core.CodeInvalidArgument, "a contribution needs one of the factory's branches and a title")
	}
	opener, ok := c.cfg.GitHub.(PullRequestOpener)
	if !ok {
		return github.PullRequest{}, core.NewError(core.CodeNotSupported, "the GitHub client cannot open pull requests")
	}
	own := c.cfg.Manifest.Repository
	pr, err := opener.CreatePullRequest(ctx, upstream, github.NewPullRequest{
		Title: req.Title, Body: req.Body, HeadRef: own.Owner + ":" + req.Branch, BaseRef: "main",
	})
	if err != nil {
		return github.PullRequest{}, err
	}
	_, err = c.Audit.Append(ctx, core.AuditEvent{
		At: c.cfg.Clock.Now(), FactoryID: c.cfg.Manifest.FactoryID, ActorID: actor.ID, ActorKind: actor.Kind, Surface: actor.Surface,
		Action: "upstream.contribute", Resource: fmt.Sprintf("repo:%s/%s", upstream.Owner, upstream.Name), Outcome: core.OutcomeAllowed,
		Details: map[string]string{"branch": req.Branch, "pull_request": fmt.Sprint(pr.Number)},
	})
	return pr, err
}

// validBranch accepts a branch name that stays inside refs/heads.
func validBranch(b string) bool {
	if b == "" || len(b) > 100 || strings.Contains(b, "..") || strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") {
		return false
	}
	return strings.IndexFunc(b, func(r rune) bool { return !branchRune(r) }) < 0
}

// branchRune is a character a contributed branch name may hold.
func branchRune(r rune) bool {
	return strings.ContainsRune("/-_.", r) || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}
