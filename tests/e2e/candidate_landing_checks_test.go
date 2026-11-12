//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// mergedCandidates lists the factory's candidate pull requests for issue
// number merged into main, with the commit each was merged at.
func (s simGitHub) mergedCandidates(t *testing.T, number int) map[int]string {
	t.Helper()
	var pulls []struct {
		Number int
		Body   string
		// MergedAt tells a merged one: GitHub's list has no merged field.
		MergedAt *string `json:"merged_at"`
		Head     struct{ Ref, SHA string }
		Base     struct{ Ref string }
	}
	out := s.must(t, http.StatusOK, "GET", "/repos/"+s.repo+"/pulls?state=all&per_page=100", "", nil)
	if err := json.Unmarshal(out, &pulls); err != nil {
		t.Fatal(err)
	}
	merged := map[int]string{}
	for _, pr := range pulls {
		if pr.MergedAt != nil && pr.Base.Ref == "main" && strings.HasPrefix(pr.Head.Ref, "sisf/") && strings.Contains(pr.Body, fmt.Sprintf("Refs #%d\n", number)) {
			merged[pr.Number] = pr.Head.SHA
		}
	}
	return merged
}

// mainHolds reports whether main carries path as it is at commit: comparing
// the commit with main finds no change to it.
func (s simGitHub) mainHolds(t *testing.T, commit, path string) bool {
	t.Helper()
	var cmp struct{ Files []struct{ Filename string } }
	out := s.must(t, http.StatusOK, "GET", fmt.Sprintf("/repos/%s/compare/%s...main", s.repo, commit), "", nil)
	if err := json.Unmarshal(out, &cmp); err != nil {
		t.Fatal(err)
	}
	for _, f := range cmp.Files {
		if f.Filename == path {
			return false
		}
	}
	return true
}

// requireLanded fails unless the issue carries one progress comment and
// exactly one candidate pull request of it was merged into main with its
// change to path.
func requireLanded(t *testing.T, gh simGitHub, number int, path string) {
	t.Helper()
	if n := gh.commentsWith(t, number, "<!-- sisf:progress "); n != 1 {
		t.Fatalf("issue #%d carries %d progress comments, want one", number, n)
	}
	merged := gh.mergedCandidates(t, number)
	if len(merged) != 1 {
		t.Fatalf("merged candidate pull requests: %v, want one", merged)
	}
	for pr, head := range merged {
		if !gh.mainHolds(t, head, path) {
			t.Fatalf("main does not carry %s as pull request #%d merged it", path, pr)
		}
	}
}
