package fakegithub_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/testkit/fakegithub"
)

func TestAPIRefusesRequestsWithoutAMatchingToken(t *testing.T) {
	t.Parallel()
	fake := fakegithub.New(fakegithub.WithToken(testToken))
	_ = fake.AddRepo(testRepo)
	for name, header := range map[string]string{"none": "", "wrong": "Bearer other", "not bearer": "Basic abc"} {
		req := httptestRequest(t, fake, "/repos/acme/app/issues", header)
		if req != http.StatusUnauthorized {
			t.Errorf("%s token: status %d, want 401", name, req)
		}
	}
	if got := httptestRequest(t, fake, "/repos/acme/app/issues", "Bearer "+testToken); got != http.StatusOK {
		t.Errorf("matching token: status %d, want 200", got)
	}
}

func TestIssueLifecycleThroughTheAPIEmitsSignedWebhooksFromTheBot(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	status, body, _ := e.call(t, "POST", "/repos/acme/app/issues", map[string]any{"title": "Crash", "body": "stack", "labels": []string{"kind:bug"}})
	if status != http.StatusCreated {
		t.Fatalf("create issue: %d %s", status, body)
	}
	if s, _, _ := e.call(t, "POST", "/repos/acme/app/issues/1/labels", map[string]any{"labels": []string{"sisf:ready", "kind:bug"}}); s != 200 {
		t.Fatalf("add labels: %d", s)
	}
	if s, _, _ := e.call(t, "POST", "/repos/acme/app/issues/1/comments", map[string]any{"body": "hello"}); s != 201 {
		t.Fatalf("comment: %d", s)
	}
	if s, _, _ := e.call(t, "DELETE", "/repos/acme/app/issues/1/labels/sisf:ready", nil); s != 200 {
		t.Fatalf("remove label: %d", s)
	}
	if s, _, _ := e.call(t, "PATCH", "/repos/acme/app/issues/1", map[string]any{"state": "closed"}); s != 200 {
		t.Fatalf("close: %d", s)
	}
	got := e.hook.deliveries()
	want := []string{"issues.opened", "issues.labeled", "issue_comment.created", "issues.unlabeled", "issues.closed"}
	if !slices.Equal(actions(got), want) {
		t.Fatalf("webhooks = %v, want %v (an already present label must not repeat)", actions(got), want)
	}
	for _, d := range got {
		if !d.SignatureOK {
			t.Errorf("delivery %s is not signed with the webhook secret", d.ID)
		}
	}
}

func TestRequestsCanActAsAPersonWithAnAssociation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, body, _ := e.call(t, "POST", "/repos/acme/app/issues", map[string]any{"title": "Idea"}, fakegithub.ActorHeader, "mallory:NONE")
	var issue struct {
		User struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"user"`
		Association string `json:"author_association"`
	}
	if err := json.Unmarshal(body, &issue); err != nil {
		t.Fatal(err)
	}
	if issue.User.Login != "mallory" || issue.User.Type != "User" || issue.Association != "NONE" {
		t.Fatalf("author = %+v", issue)
	}
}

func TestListingFiltersBySinceLabelsAndPagesWithALinkHeader(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	person := e.fake.Person("alice", "OWNER")
	for i := range 3 {
		if _, err := e.fake.OpenIssue(testRepo, person, "issue", "", map[bool][]string{true: {"sisf:ready"}}[i != 1]...); err != nil {
			t.Fatal(err)
		}
	}
	list := func(query string) (issues []struct {
		Number int `json:"number"`
	}, h http.Header) {
		_, body, h := e.call(t, "GET", "/repos/acme/app/issues?"+query, nil)
		if err := json.Unmarshal(body, &issues); err != nil {
			t.Fatal(err)
		}
		return issues, h
	}
	if got, _ := list("labels=sisf:ready&sort=updated&direction=asc"); len(got) != 2 || got[0].Number != 1 || got[1].Number != 3 {
		t.Fatalf("label filter = %+v", got)
	}
	first, _ := list("state=all")
	if len(first) != 3 || first[0].Number != 3 {
		t.Fatalf("default order is newest first, got %+v", first)
	}
	paged, h := list("state=all&per_page=2")
	if len(paged) != 2 || !strings.Contains(h.Get("Link"), `rel="next"`) || !strings.Contains(h.Get("Link"), "page=2") {
		t.Fatalf("page 1 = %+v, link %q", paged, h.Get("Link"))
	}
	since := time.Date(2026, 1, 1, 0, 0, 3, 0, time.UTC).Format(time.RFC3339)
	if got, _ := list("state=all&since=" + since); len(got) != 1 || got[0].Number != 3 {
		t.Fatalf("since filter = %+v", got)
	}
	// Reconciliation reads oldest first from a position, a page at a time;
	// the next-page link keeps the order and the position.
	since = time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC).Format(time.RFC3339)
	asc, h := list("state=all&sort=updated&direction=asc&per_page=1&since=" + since)
	if len(asc) != 1 || asc[0].Number != 2 || !strings.Contains(h.Get("Link"), "direction=asc") || !strings.Contains(h.Get("Link"), "since=") {
		t.Fatalf("oldest first since %s = %+v, link %q", since, asc, h.Get("Link"))
	}
	if next, _ := list("state=all&sort=updated&direction=asc&per_page=1&page=2&since=" + since); len(next) != 1 || next[0].Number != 3 {
		t.Fatalf("page 2 oldest first = %+v", next)
	}
}

func TestIssueListAnswersNotModifiedForAnUnchangedPage(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, _ = e.fake.OpenIssue(testRepo, e.fake.Bot(), "one", "")
	_, _, h := e.call(t, "GET", "/repos/acme/app/issues", nil)
	etag := h.Get("ETag")
	if etag == "" {
		t.Fatal("list has no ETag")
	}
	if s, _, _ := e.call(t, "GET", "/repos/acme/app/issues", nil, "If-None-Match", etag); s != http.StatusNotModified {
		t.Fatalf("unchanged list: %d, want 304", s)
	}
	_, _ = e.fake.OpenIssue(testRepo, e.fake.Bot(), "two", "")
	if s, _, _ := e.call(t, "GET", "/repos/acme/app/issues", nil, "If-None-Match", etag); s != http.StatusOK {
		t.Fatalf("changed list: %d, want 200", s)
	}
}

func TestMissingResourcesAreNotFound(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for _, p := range []string{"/repos/acme/app/issues/9", "/repos/other/repo/issues", "/repos/acme/app/pulls/1", "/repos/acme/app/issues/x"} {
		if s, _, _ := e.call(t, "GET", p, nil); s != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", p, s)
		}
	}
	if s, _, _ := e.call(t, "DELETE", "/repos/acme/app/issues/1/labels/nope", nil); s != http.StatusNotFound {
		t.Errorf("removing a label of a missing issue: %d, want 404", s)
	}
}

func TestPullRequestsReportForksAndCheckRunsAndReleasesWork(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, body, _ := e.call(t, "POST", "/repos/acme/app/pulls", map[string]any{"title": "Fix", "head": "stranger:patch", "base": "main"}, fakegithub.ActorHeader, "stranger")
	var pr struct {
		Number int `json:"number"`
		Head   struct {
			Ref  string `json:"ref"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
	}
	if err := json.Unmarshal(body, &pr); err != nil || pr.Head.Ref != "patch" || pr.Head.Repo.FullName != "stranger/app" {
		t.Fatalf("fork pull request = %s (%v)", body, err)
	}
	if s, _, _ := e.call(t, "GET", "/repos/acme/app/pulls/1", nil); s != 200 {
		t.Fatalf("get pull: %d", s)
	}
	sha := strings.Repeat("a", 40)
	s, body, _ := e.call(t, "POST", "/repos/acme/app/check-runs", map[string]any{"name": "sisf/qa", "head_sha": sha, "status": "in_progress"})
	var check struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &check); s != 201 || err != nil || check.ID == 0 {
		t.Fatalf("create check: %d %s", s, body)
	}
	if s, _, _ := e.call(t, "PATCH", "/repos/acme/app/check-runs/"+itoa(check.ID), map[string]any{"status": "completed", "conclusion": "success"}); s != 200 {
		t.Fatalf("update check: %d", s)
	}
	if runs, _ := e.fake.CheckRuns(testRepo); !slices.Equal(runs, []string{"sisf/qa:success"}) {
		t.Fatalf("check runs = %v", runs)
	}
	if s, _, _ := e.call(t, "POST", "/repos/acme/app/releases", map[string]any{"tag_name": "v1"}); s != 201 {
		t.Fatalf("release: %d", s)
	}
	s, body, _ = e.call(t, "POST", "/repos/acme/app/releases", map[string]any{"tag_name": "v1"})
	if s != 422 || !strings.Contains(string(body), "already_exists") {
		t.Fatalf("duplicate tag: %d %s", s, body)
	}
}
