package install

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/supervisor"
)

func runDoctor(t *testing.T, in *Installer) Report {
	t.Helper()
	r, err := in.Doctor(t.Context())
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	return r
}

func TestAHealthyStandaloneFactoryPassesEveryCheck(t *testing.T) {
	t.Parallel()
	in := builtStandalone(t, healthyNet(), nil)
	r := runDoctor(t, in)
	for _, c := range r.Checks {
		// The runner images are built by a later release, so they warn; and
		// Standalone states trusted_code_only, which always warns.
		if c.Verdict != VerdictOK && c.Name != "artifacts" && c.Name != "trust model" {
			t.Errorf("%s: %s %s", c.Name, c.Verdict, c.Detail)
		}
	}
	for _, name := range []string{"manifest", "release manifest", "secrets", "budget", "zone application", "zone agent", "zone control", "github", "state store", "rollback"} {
		if len(verdictOf(r, name)) == 0 {
			t.Errorf("no %q check ran", name)
		}
	}
}

func TestTheReportNeverContainsAnyTokenOrKey(t *testing.T) {
	t.Parallel()
	net := healthyNet()
	in := builtStandalone(t, net, nil)
	r := runDoctor(t, in)
	var out bytes.Buffer
	if err := r.Write(&out); err != nil {
		t.Fatal(err)
	}
	token, err := os.ReadFile(in.Layout.Secret(areaControl, "github-token"))
	if err != nil {
		t.Fatal(err)
	}
	key, _ := os.ReadFile(in.Layout.Secret(areaControl, "root-key.pem"))
	webhook, _ := os.ReadFile(in.Layout.Secret(areaControl, "webhook-secret"))
	for _, secret := range [][]byte{token, key, webhook} {
		if v := strings.TrimSpace(string(secret)); v != "" && strings.Contains(out.String(), v) {
			t.Fatal("the report contains a secret")
		}
	}
	// The token goes to the GitHub API address and nowhere else.
	for _, req := range net.seen {
		if req.Header.Get("Authorization") != "" && req.URL.Host != "127.0.0.1:7467" {
			t.Errorf("the token was sent to %s", req.URL.Host)
		}
	}
	if got := net.requestsTo("127.0.0.1:7467/repos/acme/hello-app"); len(got) != 1 || !strings.HasPrefix(got[0].Header.Get("Authorization"), "Bearer ") {
		t.Fatalf("GitHub requests = %v", got)
	}
}

func TestAFactoryThatIsDownFailsEachLiveCheckWithTheStartCommand(t *testing.T) {
	t.Parallel()
	in := builtStandalone(t, &fakeNet{routes: map[string]reply{}}, nil)
	in.SupervisorStatus = func(_ context.Context, _ string) (supervisor.Status, error) {
		return supervisor.Status{}, errors.New("no supervisor")
	}
	r := runDoctor(t, in)
	if r.OK() {
		t.Fatal("a factory that is down passed")
	}
	for _, name := range []string{"zones", "edge to app", "dispatcher", "github", "rollback"} {
		if v := verdictOf(r, name); len(v) != 1 || v[0] != VerdictFail {
			t.Errorf("%s = %v, want one failure", name, v)
		}
	}
	if !strings.Contains(detailOf(r, "zones"), "sisf up") {
		t.Errorf("the failure does not say how to start: %q", detailOf(r, "zones"))
	}
}

func TestAZoneThatReportsDownFailsOnlyThatZone(t *testing.T) {
	t.Parallel()
	net := healthyNet().with("127.0.0.1:7463/healthz", reply{code: 503,
		body: `{"factory_id":"sisf-hello-app","status":"degraded","zones":{"application":"ok","agent":"down","control":"ok"}}`})
	r := runDoctor(t, builtStandalone(t, net, nil))
	if v := verdictOf(r, "zone agent"); len(v) != 1 || v[0] != VerdictFail {
		t.Errorf("agent zone = %v", v)
	}
	for _, name := range []string{"zone application", "zone control"} {
		if v := verdictOf(r, name); len(v) != 1 || v[0] != VerdictOK {
			t.Errorf("%s = %v", name, v)
		}
	}
}

func TestGitHubProblemsAreNamedForTheOwner(t *testing.T) {
	t.Parallel()
	const route = "127.0.0.1:7467/repos/acme/hello-app"
	for name, tc := range map[string]struct {
		got  reply
		want string
	}{
		"refused token":      {reply{code: 401}, "refused"},
		"repo not shared":    {reply{code: 404}, "cannot see acme/hello-app"},
		"forbidden":          {reply{code: 403}, "cannot see acme/hello-app"},
		"read only token":    {reply{code: 200, body: `{"permissions":{"push":false,"pull":true}}`}, "not write"},
		"server error":       {reply{code: 502}, "answered 502"},
		"api unreachable":    {reply{err: errors.New("dial tcp: i/o timeout")}, "does not answer"},
		"permissions hidden": {reply{code: 200, body: `{"full_name":"acme/hello-app"}`}, ""},
	} {
		r := runDoctor(t, builtStandalone(t, healthyNet().with(route, tc.got), nil))
		v := verdictOf(r, "github")
		if tc.want == "" {
			if len(v) != 1 || v[0] != VerdictOK {
				t.Errorf("%s: verdict %v", name, v)
			}
			continue
		}
		if len(v) != 1 || v[0] != VerdictFail || !strings.Contains(detailOf(r, "github"), tc.want) {
			t.Errorf("%s: %v %q, want a failure saying %q", name, v, detailOf(r, "github"), tc.want)
		}
	}
}

func TestTheTrustedCodeOnlyModelIsAlwaysAWarning(t *testing.T) {
	t.Parallel()
	r := runDoctor(t, builtStandalone(t, healthyNet(), nil))
	if v := verdictOf(r, "trust model"); len(v) != 1 || v[0] != VerdictWarn || !strings.Contains(detailOf(r, "trust model"), "trusted_code_only") ||
		!strings.Contains(detailOf(r, "trust model"), "--agent-home") || !strings.Contains(detailOf(r, "trust model"), "--qa-home") ||
		!strings.Contains(detailOf(r, "trust model"), "--agent-binary") || !strings.Contains(detailOf(r, "trust model"), "--qa-agent-binary") {
		t.Fatalf("trust model = %v %q", v, detailOf(r, "trust model"))
	}
}

func TestATokenWithTheWorkflowScopeIsAWarning(t *testing.T) {
	t.Parallel()
	const route = "127.0.0.1:7467/repos/acme/hello-app"
	for name, tc := range map[string]struct {
		scopes []string
		want   []Verdict
	}{
		"workflow scope":      {[]string{"repo, workflow"}, []Verdict{VerdictWarn}},
		"no workflow scope":   {[]string{"repo, read:org"}, []Verdict{VerdictOK}},
		"no scopes header":    {nil, nil},
		"workflow-like scope": {[]string{"repo, workflows:read"}, []Verdict{VerdictOK}},
	} {
		got := reply{code: 200, body: repoWritable}
		if tc.scopes != nil {
			got.header = http.Header{"X-OAuth-Scopes": tc.scopes}
		}
		r := runDoctor(t, builtStandalone(t, healthyNet().with(route, got), nil))
		if v := verdictOf(r, "github token scope"); !slices.Equal(v, tc.want) {
			t.Errorf("%s: %v, want %v", name, v, tc.want)
		}
		if v := verdictOf(r, "github"); len(v) != 1 || v[0] != VerdictOK {
			t.Errorf("%s: github = %v", name, v)
		}
	}
}

func TestWithoutALastKnownGoodReleaseRollbackIsAWarning(t *testing.T) {
	t.Parallel()
	in := builtStandalone(t, healthyNet(), nil)
	in.SupervisorStatus = asThisInstall(in, supervisorWith(false))
	r := runDoctor(t, in)
	if v := verdictOf(r, "rollback"); len(v) != 1 || v[0] != VerdictWarn {
		t.Fatalf("rollback = %v", v)
	}
	if !r.OK() {
		t.Fatal("a warning failed the report")
	}
}

func TestASupervisorThatFailedItsSelfCheckFailsRollback(t *testing.T) {
	t.Parallel()
	in := builtStandalone(t, healthyNet(), nil)
	in.SupervisorStatus = asThisInstall(in, func(_ context.Context, _ string) (supervisor.Status, error) { return supervisor.Status{}, nil })
	if v := verdictOf(runDoctor(t, in), "rollback"); len(v) != 1 || v[0] != VerdictFail {
		t.Fatalf("rollback = %v", v)
	}
}

func TestAnUnconfirmedBudgetIsAWarningThatNamesTheCommand(t *testing.T) {
	t.Parallel()
	in := builtStandalone(t, healthyNet(), func(o *Options) { o.Budget = BudgetChoice{} })
	r := runDoctor(t, in)
	if v := verdictOf(r, "budget"); len(v) != 1 || v[0] != VerdictWarn || !strings.Contains(detailOf(r, "budget"), "--confirm-unlimited") {
		t.Fatalf("budget = %v %q", v, detailOf(r, "budget"))
	}
	if v := verdictOf(r, "state store"); len(v) != 1 || v[0] != VerdictWarn {
		t.Fatalf("state store = %v", v)
	}
}

func TestATamperedReleaseIsCaught(t *testing.T) {
	t.Parallel()
	for name, tamper := range map[string]func(t *testing.T, in *Installer){
		"unit repinned in the manifest": func(t *testing.T, in *Installer) {
			editFile(t, in.Layout.Config(), "sha256:", "sha256:0")
		},
		"supervisor program replaced": func(t *testing.T, in *Installer) {
			if err := os.WriteFile(in.Layout.Bin("sisf-supervisor"), []byte("evil"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"signature file damaged": func(t *testing.T, in *Installer) {
			editFile(t, in.Layout.ReleaseManifest(), `"signature"`, `"signaturf"`)
		},
		"release manifest removed": func(t *testing.T, in *Installer) {
			if err := os.Remove(in.Layout.ReleaseManifest()); err != nil {
				t.Fatal(err)
			}
		},
		"another root key": func(t *testing.T, in *Installer) {
			other := builtStandalone(t, healthyNet(), nil)
			pub, _ := os.ReadFile(other.Layout.Secret(areaControl, "root-public-key"))
			if err := os.WriteFile(in.Layout.Secret(areaControl, "root-public-key"), pub, 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		in := builtStandalone(t, healthyNet(), nil)
		tamper(t, in)
		r := runDoctor(t, in)
		if r.OK() {
			t.Errorf("%s: the report passed", name)
		}
	}
}

func TestSecretsReadableByOthersFailTheReport(t *testing.T) {
	t.Parallel()
	in := builtStandalone(t, healthyNet(), nil)
	if err := os.Chmod(in.Layout.Secret(areaControl, "webhook-secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v := verdictOf(runDoctor(t, in), "secrets"); len(v) == 0 || v[0] != VerdictFail {
		t.Fatalf("secrets = %v", v)
	}
}

func TestUnbuiltRunnersAreAWarningThatNamesThem(t *testing.T) {
	t.Parallel()
	r := runDoctor(t, builtStandalone(t, healthyNet(), nil))
	if v := verdictOf(r, "artifacts"); len(v) != 1 || v[0] != VerdictWarn || !strings.Contains(detailOf(r, "artifacts"), "qa-runner") {
		t.Fatalf("artifacts = %v %q", v, detailOf(r, "artifacts"))
	}
}

func TestADirectoryWithoutAnInstallFailsOnTheManifestAlone(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	r := runDoctor(t, in)
	if r.OK() || len(r.Checks) != 1 || !strings.Contains(r.Checks[0].Detail, "sisf init") {
		t.Fatalf("report = %+v", r.Checks)
	}
}

func TestOnlySecureAPIAddressesReceiveTheToken(t *testing.T) {
	t.Parallel()
	in := builtStandalone(t, healthyNet(), nil)
	editFile(t, in.Layout.Config(), "http://127.0.0.1:7467", "http://api.example.test")
	net := in.HTTP.Transport.(*fakeNet)
	r := runDoctor(t, in)
	if v := verdictOf(r, "github"); len(v) != 1 || v[0] != VerdictFail {
		t.Fatalf("github = %v", v)
	}
	for _, req := range net.seen {
		if req.URL.Host == "api.example.test" {
			t.Fatal("the token was sent over clear text to a remote host")
		}
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	t.Parallel()
	net := healthyNet()
	in := builtStandalone(t, net, nil)
	in.HTTP = &http.Client{Transport: net}
	net.routes["127.0.0.1:7467/repos/acme/hello-app"] = reply{code: 302}
	if v := verdictOf(runDoctor(t, in), "github"); len(v) != 1 || v[0] != VerdictFail {
		t.Fatalf("github = %v", v)
	}
}

// editFile replaces the first occurrence of old in a file and fails the
// test when there is none.
func editFile(t *testing.T, path, old, repl string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), old) {
		t.Fatalf("%s has no %q", path, old)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), old, repl, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
}
