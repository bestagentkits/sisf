package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// runCLI runs the real command registry and returns the exit status and what
// the command wrote.
func runCLI(t *testing.T, stdin string, args ...string) (status int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	status = commands.run(context.Background(), &Env{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errOut}, args)
	return status, out.String(), errOut.String()
}

// seen is one request a fake factory received.
type seen struct {
	path, auth, ctype string
	body              map[string]any
}

// fakeFactory answers every request with answer and records what it saw.
type fakeFactory struct {
	srv    *httptest.Server
	mu     sync.Mutex
	calls  []seen
	status int
	reply  string
}

func newFakeFactory(t *testing.T, status int, reply string) *fakeFactory {
	t.Helper()
	f := &fakeFactory{status: status, reply: reply}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s := seen{path: r.URL.Path, auth: r.Header.Get("Authorization"), ctype: r.Header.Get("Content-Type")}
		_ = json.Unmarshal(raw, &s.body)
		f.mu.Lock()
		f.calls = append(f.calls, s)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.reply)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeFactory) seen() []seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seen(nil), f.calls...)
}

// isolate points the CLI at an empty SISF home and clears the environment it
// reads, so a developer's own login never leaks into a test.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(homeEnv, dir)
	t.Setenv(tokenEnv, "")
	t.Setenv(controlURLEnv, "")
	return dir
}

func TestCallSendsTheInputAndPrintsTheResult(t *testing.T) {
	isolate(t)
	f := newFakeFactory(t, 200, `{"output":{"ok":true}}`)
	t.Setenv(tokenEnv, "sisf_pat_testtoken")
	status, out, errOut := runCLI(t, "", "call", "runs.explain", "--url", f.srv.URL, "--input", `{"issue":"acme/app#1"}`,
		"--dry-run", "--approval", "apr_1", "--idempotency-key", "k1")
	if status != 0 || errOut != "" || !strings.Contains(out, "\"ok\": true") {
		t.Fatalf("status %d out %q err %q", status, out, errOut)
	}
	got := f.seen()
	if len(got) != 1 || got[0].path != "/api/v1/actions/runs.explain" || got[0].auth != "Bearer sisf_pat_testtoken" || got[0].ctype != "application/json" {
		t.Fatalf("request: %+v", got)
	}
	want := map[string]any{"input": map[string]any{"issue": "acme/app#1"}, "dry_run": true, "approval_id": "apr_1", "idempotency_key": "k1"}
	if gotJSON, wantJSON := marshal(t, got[0].body), marshal(t, want); gotJSON != wantJSON {
		t.Fatalf("body %s, want %s", gotJSON, wantJSON)
	}
	if strings.Contains(out+errOut, "sisf_pat_testtoken") {
		t.Fatal("the token was printed")
	}
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestCallReadsInputFromStandardInputAndRejectsBadJSON(t *testing.T) {
	isolate(t)
	f := newFakeFactory(t, 200, `{}`)
	t.Setenv(tokenEnv, "sisf_pat_t")
	if status, _, errOut := runCLI(t, `{"id":"a1"}`, "call", "approvals.get", "--url", f.srv.URL, "--input", "-"); status != 0 {
		t.Fatalf("stdin input: %d %s", status, errOut)
	}
	if got := f.seen(); len(got) != 1 || marshal(t, got[0].body["input"]) != `{"id":"a1"}` {
		t.Fatalf("input: %+v", got)
	}
	for _, args := range [][]string{{"call", "x.y", "--url", f.srv.URL, "--input", "{nope"}, {"call", "BadName", "--url", f.srv.URL}, {"call"}} {
		if status, _, _ := runCLI(t, "", args...); status != exitUsage {
			t.Errorf("%v exited %d, want usage", args, status)
		}
	}
	if len(f.seen()) != 1 {
		t.Fatal("a malformed call reached the server")
	}
}

func TestCallFailureExitsNonZeroWithTheTypedCode(t *testing.T) {
	isolate(t)
	f := newFakeFactory(t, 403, `{"error":{"code":"APPROVAL_REQUIRED","message":"file a request first","next_actions":[{"action":"approvals.list","description":"see the inbox"}]}}`)
	t.Setenv(tokenEnv, "sisf_pat_t")
	status, out, errOut := runCLI(t, "", "call", "budget.set", "--url", f.srv.URL)
	if status != exitFailure || out != "" || !strings.Contains(errOut, "APPROVAL_REQUIRED") || !strings.Contains(errOut, "next: approvals.list") {
		t.Fatalf("status %d out %q err %q", status, out, errOut)
	}
	g := newFakeFactory(t, 502, "<html>bad gateway</html>")
	if status, _, errOut := runCLI(t, "", "call", "budget.get", "--url", g.srv.URL); status != exitFailure || !strings.Contains(errOut, string(core.CodeUnavailable)) {
		t.Fatalf("untyped failure: %d %s", status, errOut)
	}
	if status, _, errOut := runCLI(t, "", "call", "budget.get", "--url", "http://127.0.0.1:1"); status != exitFailure || !strings.Contains(errOut, "UNAVAILABLE") {
		t.Fatalf("unreachable factory: %d %s", status, errOut)
	}
}

func TestStoredLoginIsUsedOnlyForItsOwnURLAndUntilItExpires(t *testing.T) {
	dir := isolate(t)
	f := newFakeFactory(t, 200, `{}`)
	other := newFakeFactory(t, 200, `{}`)
	now := time.Now()
	path := filepath.Join(dir, credentialsFile)
	store := func(url string, expires time.Time) {
		t.Helper()
		if err := saveCredentials(path, credentials{URL: url, Token: "sisf_pat_stored", ExpiresAt: expires}); err != nil {
			t.Fatal(err)
		}
	}
	store(f.srv.URL, now.Add(time.Hour))
	if status, _, errOut := runCLI(t, "", "call", "factory.status"); status != 0 || errOut != "" {
		t.Fatalf("stored login as the default URL: %d %s", status, errOut)
	}
	if got := f.seen(); len(got) != 1 || got[0].auth != "Bearer sisf_pat_stored" {
		t.Fatalf("stored token not sent: %+v", got)
	}
	if status, _, errOut := runCLI(t, "", "call", "factory.status", "--url", other.srv.URL); status != exitFailure || !strings.Contains(errOut, "UNAUTHENTICATED") {
		t.Fatalf("stored login sent to another URL: %d %s", status, errOut)
	}
	if len(other.seen()) != 0 {
		t.Fatal("the stored token reached a different server")
	}
	store(f.srv.URL, now.Add(-time.Minute))
	if status, _, errOut := runCLI(t, "", "call", "factory.status"); status != exitFailure || !strings.Contains(errOut, "expired") {
		t.Fatalf("expired login: %d %s", status, errOut)
	}
	if len(f.seen()) != 1 {
		t.Fatal("an expired token was sent")
	}
}

func TestCallNeverFollowsARedirectWithTheToken(t *testing.T) {
	isolate(t)
	target := newFakeFactory(t, 200, `{}`)
	redirector := httptest.NewServer(http.RedirectHandler(target.srv.URL, http.StatusTemporaryRedirect))
	t.Cleanup(redirector.Close)
	t.Setenv(tokenEnv, "sisf_pat_t")
	if status, _, _ := runCLI(t, "", "call", "factory.status", "--url", redirector.URL); status != exitFailure {
		t.Fatalf("redirect answered with %d", status)
	}
	if len(target.seen()) != 0 {
		t.Fatal("the redirect was followed")
	}
}

func TestPlainHTTPToARemoteHostIsRefused(t *testing.T) {
	isolate(t)
	t.Setenv(tokenEnv, "sisf_pat_t")
	if status, _, errOut := runCLI(t, "", "call", "factory.status", "--url", "http://factory.example.test:7463"); status != exitUsage || !strings.Contains(errOut, "plain http") {
		t.Fatalf("remote http: %d %s", status, errOut)
	}
	if status, _, _ := runCLI(t, "", "call", "factory.status", "--url", "ftp://x"); status != exitUsage {
		t.Fatalf("a non-http URL: %d", status)
	}
}

func TestAliasesCallTheirActionsWithTheInputTheyBuild(t *testing.T) {
	isolate(t)
	f := newFakeFactory(t, 200, `{}`)
	t.Setenv(tokenEnv, "sisf_pat_t")
	cases := []struct {
		args   []string
		action string
		input  string
	}{
		{[]string{"status"}, "factory.status", `{}`},
		{[]string{"issues", "--state", "blocked", "--state", "ready", "--limit", "5"}, "issues.list", `{"limit":5,"states":["blocked","ready"]}`},
		{[]string{"runs", "explain", "acme/app#12"}, "runs.explain", `{"issue":"acme/app#12"}`},
		{[]string{"runs", "inspect", "run_9"}, "runs.inspect", `{"run_id":"run_9"}`},
		{[]string{"approvals", "list", "--status", "pending"}, "approvals.list", `{"statuses":["pending"]}`},
		{[]string{"approvals", "get", "apr_1"}, "approvals.get", `{"id":"apr_1"}`},
		{[]string{"approvals", "decide", "apr_1", "--decision", "approve", "--nonce", "n", "--subject-digest", "d", "--policy-revision", "p",
			"--reason", "ok", "--budget-cap", "100", "--dry-run"}, "approvals.decide",
			`{"conditions":{"budget_cap":100},"decision":"approve","nonce":"n","policy_revision":"p","reason":"ok","request_id":"apr_1","subject_digest":"d"}`},
	}
	for i, c := range cases {
		if status, _, errOut := runCLI(t, "", append(c.args, "--url", f.srv.URL)...); status != 0 {
			t.Fatalf("%v: %d %s", c.args, status, errOut)
		}
		got := f.seen()[i]
		if got.path != "/api/v1/actions/"+c.action || marshal(t, got.body["input"]) != c.input {
			t.Errorf("%v called %s with %s, want %s %s", c.args, got.path, marshal(t, got.body["input"]), c.action, c.input)
		}
	}
	if f.seen()[len(cases)-1].body["dry_run"] != true {
		t.Error("decide did not pass --dry-run")
	}
	for _, args := range [][]string{{"approvals", "decide", "apr_1", "--decision", "approve"}, {"runs", "explain"}, {"runs", "explain", "a", "b"}} {
		if status, _, _ := runCLI(t, "", append(args, "--url", f.srv.URL)...); status != exitUsage {
			t.Errorf("%v exited %d, want usage", args, status)
		}
	}
}

func TestStoredLoginFileIsOwnerOnlyAndReplacedWhole(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home")
	path := filepath.Join(dir, credentialsFile)
	for i := range 2 {
		if err := saveCredentials(path, credentials{URL: "http://127.0.0.1:1", Token: "sisf_pat_" + strings.Repeat("x", i+1), ExpiresAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("login file mode %v (%v)", info, err)
		}
	}
	if d, err := os.Stat(dir); err != nil || d.Mode().Perm()&0o077 != 0 {
		t.Fatalf("home directory mode %v (%v)", d, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
	got, ok, err := loadCredentials(path)
	if err != nil || !ok || got.Token != "sisf_pat_xx" {
		t.Fatalf("stored login: %+v %v %v", got, ok, err)
	}
}

// The default control URL is a fixed loopback port that another factory on
// the same host may hold: with an install in the SISF home, a token or a
// login request goes only to a server that names this install's factory.
func TestATokenIsNeverSentToAnotherFactory(t *testing.T) {
	isolate(t)
	home := initSim(t, "standalone")
	t.Setenv(homeEnv, home)
	t.Setenv(tokenEnv, "token-of-this-install")
	for factory, want := range map[string]int{"sisf-other": exitFailure, "": exitFailure, "sisf-hello-app": exitOK} {
		f := newFakeFactory(t, http.StatusOK, `{"factory_id":"`+factory+`","output":{}}`)
		status, _, stderr := runCLI(t, "", "status", "--url", f.srv.URL)
		if status != want {
			t.Errorf("factory %q: status %d, want %d (%s)", factory, status, want, stderr)
		}
		for _, s := range f.seen() {
			if s.auth != "" && factory != "sisf-hello-app" {
				t.Errorf("factory %q received the token at %s", factory, s.path)
			}
		}
		if factory == "sisf-other" && !strings.Contains(stderr, "is factory sisf-other, not sisf-hello-app") {
			t.Errorf("the refusal does not name both factories: %s", stderr)
		}
		if factory == "sisf-hello-app" {
			continue
		}
		status, _, _ = runCLI(t, "", "login", "--url", f.srv.URL)
		if status != exitFailure {
			t.Errorf("factory %q: login status %d, want a refusal", factory, status)
		}
		for _, s := range f.seen() {
			if s.path == deviceStartPath {
				t.Errorf("a login request was sent to factory %q", factory)
			}
		}
	}
}
