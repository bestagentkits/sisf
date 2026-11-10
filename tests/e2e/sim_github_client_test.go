//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// simGitHub drives the fake GitHub of a running factory over HTTP, as the
// people of the repository and as its simulation controls.
type simGitHub struct {
	base string
	repo string // owner/name
	http *http.Client
}

func newSimGitHub(base, repo string) simGitHub {
	return simGitHub{base: strings.TrimRight(base, "/"), repo: repo, http: &http.Client{Timeout: 30 * time.Second}}
}

// maintainer is how a person with write access shows on the fake.
const maintainer = "maintainer:MEMBER"

// call sends one request; actor, when set, is the person making it.
func (s simGitHub) call(t *testing.T, method, path, actor string, body any) (int, []byte) {
	t.Helper()
	var in io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		in = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, s.base+path, in)
	if err != nil {
		t.Fatal(err)
	}
	// The fake accepts any token; the factory's own token is never needed here.
	req.Header.Set("Authorization", "Bearer e2e-person")
	req.Header.Set("Content-Type", "application/json")
	if actor != "" {
		req.Header.Set("X-Sim-Actor", actor)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		t.Fatalf("fake GitHub %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, out
}

func (s simGitHub) must(t *testing.T, want int, method, path, actor string, body any) []byte {
	t.Helper()
	status, out := s.call(t, method, path, actor, body)
	if status != want {
		t.Fatalf("fake GitHub %s %s answered %d: %s", method, path, status, out)
	}
	return out
}

// commit stores files as one commit of the repository and returns its SHA.
func (s simGitHub) commit(t *testing.T, files map[string]string) string {
	t.Helper()
	var made struct{ SHA string }
	if err := json.Unmarshal(s.must(t, http.StatusCreated, "POST", "/_sim/commits", "", map[string]any{"repo": s.repo, "files": files}), &made); err != nil {
		t.Fatal(err)
	}
	return made.SHA
}

func (s simGitHub) openIssue(t *testing.T, actor, title, body string, labels ...string) int {
	t.Helper()
	var issue struct{ Number int }
	out := s.must(t, http.StatusCreated, "POST", "/repos/"+s.repo+"/issues", actor, map[string]any{"title": title, "body": body, "labels": labels})
	if err := json.Unmarshal(out, &issue); err != nil || issue.Number == 0 {
		t.Fatalf("created issue %s: %v", out, err)
	}
	return issue.Number
}

func (s simGitHub) comment(t *testing.T, number int, actor, body string) {
	t.Helper()
	s.must(t, http.StatusCreated, "POST", fmt.Sprintf("/repos/%s/issues/%d/comments", s.repo, number), actor, map[string]any{"body": body})
}

// commentsWith counts the comments of issue number that contain text.
func (s simGitHub) commentsWith(t *testing.T, number int, text string) int {
	t.Helper()
	var comments []struct{ Body string }
	out := s.must(t, http.StatusOK, "GET", fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100", s.repo, number), "", nil)
	if err := json.Unmarshal(out, &comments); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range comments {
		if strings.Contains(c.Body, text) {
			n++
		}
	}
	return n
}

// faults sets the delivery faults of the fake: hold, release, duplicate.
func (s simGitHub) faults(t *testing.T, f map[string]any) {
	t.Helper()
	s.must(t, http.StatusNoContent, "POST", "/_sim/faults", "", f)
}

// delivery is one webhook delivery as GitHub lists it.
type delivery struct {
	ID     int    `json:"id"`
	GUID   string `json:"guid"`
	Event  string `json:"event"`
	Action string `json:"action"`
	Status int    `json:"status_code"`
}

func (s simGitHub) deliveries(t *testing.T) []delivery {
	t.Helper()
	var out []delivery
	if err := json.Unmarshal(s.must(t, http.StatusOK, "GET", "/app/hook/deliveries", "", nil), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// redeliverAll sends every delivery the fake ever made once more, with its
// original ID, as GitHub's "redeliver" does.
func (s simGitHub) redeliverAll(t *testing.T, ds []delivery) {
	t.Helper()
	for _, d := range ds {
		status, out := s.call(t, "POST", "/app/hook/deliveries/"+url.PathEscape(fmt.Sprint(d.ID))+"/attempts", "", nil)
		if status/100 != 2 {
			t.Fatalf("redeliver %d answered %d: %s", d.ID, status, out)
		}
	}
}
