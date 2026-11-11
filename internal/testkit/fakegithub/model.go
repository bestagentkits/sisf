package fakegithub

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// User is a GitHub account. Association is how GitHub relates it to the
// repository, for example "OWNER", "COLLABORATOR" or "NONE".
type User struct {
	ID          int64
	Login       string
	Bot         bool
	Association string
}

func (u User) assoc() string {
	if u.Association == "" {
		return "NONE"
	}
	return u.Association
}

type jUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}

func (u User) json() jUser {
	kind := "User"
	if u.Bot {
		kind = "Bot"
	}
	return jUser{ID: u.ID, Login: u.Login, Type: kind}
}

type jLabel struct {
	Name string `json:"name"`
}

type jOwner struct {
	Login string `json:"login"`
}

type jRepo struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Owner    jOwner `json:"owner"`
}

func repoJSON(owner, name string) jRepo {
	return jRepo{Name: name, FullName: owner + "/" + name, Owner: jOwner{Login: owner}}
}

type jIssue struct {
	Number            int       `json:"number"`
	Title             string    `json:"title"`
	Body              string    `json:"body"`
	State             string    `json:"state"`
	Labels            []jLabel  `json:"labels"`
	User              jUser     `json:"user"`
	AuthorAssociation string    `json:"author_association"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	PullRequest       *struct{} `json:"pull_request,omitempty"`
}

type jComment struct {
	ID                int64     `json:"id"`
	Body              string    `json:"body"`
	User              jUser     `json:"user"`
	AuthorAssociation string    `json:"author_association"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type jBranch struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo *jRepo `json:"repo"`
}

type jPull struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
	// Merged is in the single pull request's answer only; the list answers
	// carry merged_at alone, as GitHub's do.
	Merged            *bool      `json:"merged,omitempty"`
	MergedAt          *time.Time `json:"merged_at"`
	Head              jBranch    `json:"head"`
	Base              jBranch    `json:"base"`
	User              jUser      `json:"user"`
	AuthorAssociation string     `json:"author_association"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// pull is the pull-request half of an issue entry.
type pull struct {
	headRef, baseRef    string
	headOwner, headName string
	headSHA, baseSHA    string
	draft, merged       bool
	mergedAt            time.Time
}

type issue struct {
	number           int
	title, body      string
	state            string
	labels           []string
	author           User
	created, updated time.Time
	pr               *pull
}

type comment struct {
	id               int64
	issue            int
	body             string
	author           User
	created, updated time.Time
}

type checkRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	HeadSHA    string `json:"head_sha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	Summary    string `json:"-"`
	DetailsURL string `json:"details_url,omitempty"`
}

type release struct {
	Tag    string `json:"tag_name"`
	Name   string `json:"name,omitempty"`
	Body   string `json:"body,omitempty"`
	Target string `json:"target_commitish,omitempty"`
}

type repo struct {
	owner, name string
	nextNumber  int
	issues      map[int]*issue
	comments    []*comment
	checks      []*checkRun
	releases    []*release
	// commits holds the files of each commit made with Commit, by SHA.
	commits map[string]map[string][]byte
	// entryKinds marks "commit:path" entries that are not plain files.
	entryKinds map[string]string
	// readOnly takes write access away from the token the fake answers
	// for: the repository then reports no push permission.
	readOnly bool
}

func repoKey(owner, name string) string { return strings.ToLower(owner + "/" + name) }

// splitRepo splits "owner/name".
func splitRepo(full string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(full, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("repository %q must be owner/name", full)
	}
	return owner, name, nil
}

// fakeSHA derives a stable 40-character commit id from a seed.
func fakeSHA(seed string) string {
	sum := sha1.Sum([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func (i *issue) hasLabel(name string) bool {
	for _, l := range i.labels {
		if l == name {
			return true
		}
	}
	return false
}
