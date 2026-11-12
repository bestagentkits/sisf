package fakegithub_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestACommitServesExactlyItsFilesAtItsSHA(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	files := map[string]string{"AGENTS.md": "# rules\n", ".sisf/factory.yaml": strings.Repeat("x", 200)}
	status, body, _ := e.call(t, "POST", "/_sim/commits", map[string]any{"repo": testRepo, "files": files})
	if status != http.StatusCreated {
		t.Fatalf("commit: %d %s", status, body)
	}
	var made struct{ SHA string }
	if err := json.Unmarshal(body, &made); err != nil || len(made.SHA) != 40 {
		t.Fatalf("commit answer %s: %v", body, err)
	}
	again, err := e.fake.Commit(testRepo, files)
	if err != nil || again != made.SHA {
		t.Fatalf("the same files made another commit: %s, %v", again, err)
	}

	status, body, _ = e.call(t, "GET", "/repos/acme/app/contents/.sisf/factory.yaml?ref="+made.SHA, nil)
	if status != http.StatusOK {
		t.Fatalf("read: %d %s", status, body)
	}
	var file struct{ Type, Encoding, Content string }
	if err := json.Unmarshal(body, &file); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if err != nil || string(raw) != files[".sisf/factory.yaml"] || file.Type != "file" || file.Encoding != "base64" {
		t.Fatalf("content = %q (%v), %+v", raw, err, file)
	}

	for name, path := range map[string]string{
		"a file the commit lacks": "/repos/acme/app/contents/README.md?ref=" + made.SHA,
		"a branch name":           "/repos/acme/app/contents/AGENTS.md?ref=main",
		"no ref":                  "/repos/acme/app/contents/AGENTS.md",
		"another repository":      "/repos/acme/other/contents/AGENTS.md?ref=" + made.SHA,
	} {
		if status, _, _ := e.call(t, "GET", path, nil); status != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", name, status)
		}
	}
}

func TestACommitRefusesPathsThatLeaveTheRepository(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for _, path := range []string{"../etc/passwd", "/abs", "a//b", "a/./b", ""} {
		if _, err := e.fake.Commit(testRepo, map[string]string{path: "x"}); err == nil {
			t.Errorf("path %q was accepted", path)
		}
	}
	if _, err := e.fake.Commit("acme/missing", map[string]string{"a": "x"}); err == nil {
		t.Error("a commit to a missing repository was accepted")
	}
}

func TestTheRepositoryIsVisibleAndWritableToTheFactoryOnly(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	status, body, _ := e.call(t, "GET", "/repos/acme/app", nil)
	var repo struct {
		FullName    string `json:"full_name"`
		Permissions struct{ Push bool }
	}
	if status != http.StatusOK || json.Unmarshal(body, &repo) != nil || repo.FullName != testRepo || !repo.Permissions.Push {
		t.Fatalf("repository: %d %s", status, body)
	}
	if status, _, _ := e.call(t, "GET", "/repos/acme/other", nil); status != http.StatusNotFound {
		t.Fatalf("an unknown repository answered %d", status)
	}
}
