package github_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

func asCore(err error, target **core.Error) bool {
	typed, ok := err.(*core.Error)
	if ok {
		*target = typed
	}
	return ok
}

func TestListingFollowsEveryPageAndKeepsPullRequestsOut(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	alice := f.person("alice")
	for i := range 205 {
		mustOpen(t, f, alice, fmt.Sprintf("issue %d", i))
	}
	if _, err := f.fake.OpenPullRequest(testRepo, alice, fakePull()); err != nil {
		t.Fatal(err)
	}
	issues, err := f.client.ListIssues(bg, repoRef, github.ListIssuesOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 205 {
		t.Fatalf("%d issues, want 205 (pull requests are not issues)", len(issues))
	}
	for i := 1; i < len(issues); i++ {
		if issues[i].UpdatedAt.Before(issues[i-1].UpdatedAt) {
			t.Fatal("issues must come oldest update first")
		}
	}
}

func TestListingStopsAtThePageCeiling(t *testing.T) {
	t.Parallel()
	f := newFactory(t, func(c *github.ClientConfig) { c.MaxPages = 2 })
	alice := f.person("alice")
	for i := range 250 {
		mustOpen(t, f, alice, fmt.Sprintf("issue %d", i))
	}
	if _, err := f.client.ListIssues(bg, repoRef, github.ListIssuesOptions{}); !core.HasCode(err, core.CodeLimitExceeded) {
		t.Fatalf("err = %v, want LIMIT_EXCEEDED rather than a silently truncated listing", err)
	}
}

func TestNextPageLinkToAnotherHostIsNeverFollowed(t *testing.T) {
	t.Parallel()
	var stolen atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			stolen.Add(1)
		}
	}))
	defer evil.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", fmt.Sprintf(`<%s/page2>; rel="next"`, evil.URL))
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c, _ := github.NewRESTClient(github.ClientConfig{BaseURL: srv.URL, Token: github.StaticToken(core.NewSecretValue([]byte(testToken)))})
	if _, err := c.ListIssues(bg, repoRef, github.ListIssuesOptions{}); err == nil {
		t.Fatal("a pagination link off the API host must fail the listing")
	}
	if stolen.Load() != 0 {
		t.Fatal("the token was sent to a host named by a response")
	}
}
