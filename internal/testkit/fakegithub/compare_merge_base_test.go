package fakegithub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Like GitHub's three-dot comparison, compare lists what head changed
// since it parted from base: nothing when base already contains head,
// however much base changed since.
func TestCompareListsWhatHeadChangedSinceTheMergeBase(t *testing.T) {
	t.Parallel()
	fake := New(WithToken("token"))
	if err := fake.AddRepo("acme/app"); err != nil {
		t.Fatal(err)
	}
	data := NewGitData(fake)
	srv := httptest.NewServer(data)
	t.Cleanup(srv.Close)
	call := func(method, path string, body any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, srv.URL+"/repos/acme/app"+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode >= 300 {
			t.Fatalf("%s %s answered %d, %v", method, path, resp.StatusCode, err)
		}
		return out
	}
	// commit makes a commit on parent that writes one file.
	commit := func(parent, path, content string) string {
		t.Helper()
		tree := call(http.MethodPost, "/git/trees", map[string]any{"base_tree": parent,
			"tree": []map[string]any{{"path": path, "mode": "100644", "type": "blob", "content": content}}})["sha"].(string)
		return call(http.MethodPost, "/git/commits", map[string]any{"message": "write " + path, "tree": tree, "parents": []string{parent}})["sha"].(string)
	}
	listed := func(base, head string) []any {
		t.Helper()
		files, _ := call(http.MethodGet, "/compare/"+base+"..."+head, nil)["files"].([]any)
		return files
	}
	root, err := fake.Commit("acme/app", map[string]string{"a.txt": "a\n"})
	if err != nil {
		t.Fatal(err)
	}
	change := commit(root, "b.txt", "b\n")
	later := commit(change, "c.txt", "c\n")
	if files := listed(later, change); len(files) != 0 {
		t.Fatalf("a base that contains head lists %v", files)
	}
	if files := listed(root, change); len(files) != 1 {
		t.Fatalf("a base without head lists %v, want b.txt", files)
	}
}
