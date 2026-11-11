package github

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// The wire types mirror the JSON GitHub sends, in webhook payloads and in REST
// responses alike, and convert it to the types of this package. Everything
// decoded here is untrusted data.

type wireUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}

func (u wireUser) user() User { return User{ID: u.ID, Login: u.Login, Bot: u.Type == "Bot"} }

type wireLabel struct {
	Name string `json:"name"`
}

type wireRepo struct {
	Name  string `json:"name"`
	Owner struct {
		Login string `json:"login"`
	} `json:"owner"`
}

func (r wireRepo) ref() RepoRef { return RepoRef{Owner: r.Owner.Login, Name: r.Name} }

type wireIssue struct {
	Number            int             `json:"number"`
	Title             string          `json:"title"`
	Body              string          `json:"body"`
	State             string          `json:"state"`
	Labels            []wireLabel     `json:"labels"`
	User              wireUser        `json:"user"`
	AuthorAssociation string          `json:"author_association"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	PullRequest       json.RawMessage `json:"pull_request"`
}

// parseState reads an issue or pull-request state. Anything but "open" is
// treated as closed, so an unknown value never revives work.
func parseState(s string) IssueState {
	if s == string(IssueOpen) {
		return IssueOpen
	}
	return IssueClosed
}

func (w wireIssue) issue(repo RepoRef) Issue {
	issue := Issue{
		Ref: repo.Issue(w.Number), Title: w.Title, Body: w.Body, State: parseState(w.State),
		Author: w.User.user(), AuthorAssociation: AuthorAssociation(w.AuthorAssociation),
		CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}
	for _, l := range w.Labels {
		issue.Labels = append(issue.Labels, Label(l))
	}
	return issue
}

// isPullRequest reports whether the issues API entry is really a pull request.
func (w wireIssue) isPullRequest() bool {
	return len(w.PullRequest) > 0 && string(w.PullRequest) != "null"
}

type wireComment struct {
	ID                int64     `json:"id"`
	Body              string    `json:"body"`
	User              wireUser  `json:"user"`
	AuthorAssociation string    `json:"author_association"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (w wireComment) comment(ref core.IssueRef) Comment {
	return Comment{
		ID: w.ID, Issue: ref, Body: w.Body, Author: w.User.user(),
		AuthorAssociation: AuthorAssociation(w.AuthorAssociation),
		CreatedAt:         w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}
}

type wireBranch struct {
	Ref  string    `json:"ref"`
	SHA  string    `json:"sha"`
	Repo *wireRepo `json:"repo"`
}

type wirePull struct {
	Number            int        `json:"number"`
	HTMLURL           string     `json:"html_url"`
	Title             string     `json:"title"`
	Body              string     `json:"body"`
	State             string     `json:"state"`
	Draft             bool       `json:"draft"`
	Merged            bool       `json:"merged"`
	Head              wireBranch `json:"head"`
	Base              wireBranch `json:"base"`
	User              wireUser   `json:"user"`
	AuthorAssociation string     `json:"author_association"`
}

// pull converts the payload. A head repository that is missing (a deleted
// fork) or differs from the base repository counts as a fork, so an unknown
// origin is never trusted.
func (w wirePull) pull(repo RepoRef) PullRequest {
	pr := PullRequest{
		Repo: repo, Number: w.Number, URL: w.HTMLURL, Title: w.Title, Body: w.Body, State: parseState(w.State),
		Draft: w.Draft, Merged: w.Merged, HeadRef: w.Head.Ref, HeadSHA: core.CommitSHA(w.Head.SHA),
		BaseRef: w.Base.Ref, BaseSHA: core.CommitSHA(w.Base.SHA), IsFork: true,
		Author: w.User.user(), AuthorAssociation: AuthorAssociation(w.AuthorAssociation),
	}
	if w.Head.Repo != nil {
		pr.HeadRepo = w.Head.Repo.ref()
		pr.IsFork = !sameRepo(pr.HeadRepo, repo)
	}
	return pr
}

// sameRepo compares repositories the way GitHub does, ignoring case.
func sameRepo(a, b RepoRef) bool {
	return strings.EqualFold(a.Owner, b.Owner) && strings.EqualFold(a.Name, b.Name)
}
