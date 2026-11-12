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
	// A merge whose first parent is the commit the branch was made on: the
	// merge base of the branch and the merge is the branch itself, not the
	// older commit both descend from, which a search reaches first.
	branch := commit(later, "d.txt", "d\n")
	tree := call(http.MethodPost, "/git/trees", map[string]any{"base_tree": later,
		"tree": []map[string]any{{"path": "d.txt", "mode": "100644", "type": "blob", "content": "d\n"}}})["sha"].(string)
	merge := call(http.MethodPost, "/git/commits", map[string]any{"message": "merge", "tree": tree, "parents": []string{later, branch}})["sha"].(string)
	if got := data.mergeBase("acme/app", branch, merge); got != branch {
		t.Fatalf("the merge base of a merged branch and the merge is %s, want the branch %s", got, branch)
	}
	if files := listed(branch, merge); len(files) != 0 {
		t.Fatalf("the merge lists %v since the branch, want nothing", files)
	}
}

// Like a git commit, a commit made on a parent is a commit of its own even
// when its files are those of a commit made without one: the parentless
// commit keeps its SHA and its history, and the new one descends from its
// parent.
func TestACommitOnAParentIsNotTheParentlessCommitOfTheSameFiles(t *testing.T) {
	t.Parallel()
	fake := New(WithToken("token"))
	if err := fake.AddRepo("acme/app"); err != nil {
		t.Fatal(err)
	}
	data := NewGitData(fake)
	srv := httptest.NewServer(data)
	t.Cleanup(srv.Close)
	call := func(path string, body any) string {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/repos/acme/app"+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out struct{ SHA string }
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST %s answered %d, %v", path, resp.StatusCode, err)
		}
		return out.SHA
	}
	seed, err := fake.Commit("acme/app", map[string]string{"a.txt": "a\n"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := fake.Commit("acme/app", map[string]string{"b.txt": "b\n"})
	if err != nil {
		t.Fatal(err)
	}
	tree := call("/git/trees", map[string]any{"tree": []map[string]any{{"path": "a.txt", "mode": "100644", "type": "blob", "content": "a\n"}}})
	onOther := call("/git/commits", map[string]any{"message": "restore a.txt", "tree": tree, "parents": []string{other}})
	if onOther == seed {
		t.Fatalf("the commit on %s took the SHA of the parentless commit of the same files", other)
	}
	if got := data.mergeBase("acme/app", onOther, other); got != other {
		t.Fatalf("the commit on %s does not descend from it: merge base %s", other, got)
	}
	if got := data.mergeBase("acme/app", seed, other); got != seed {
		t.Fatalf("the parentless commit was given a history: merge base %s", got)
	}
}
