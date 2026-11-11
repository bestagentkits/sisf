package install

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// fixedStart is the instant every test clock begins at.
var fixedStart = time.Date(2026, 11, 10, 9, 0, 0, 0, time.FixedZone("ICT", 7*3600))

// newTestInstaller returns an installer for a fresh temporary directory
// whose clock only the test moves and whose programs are fakes.
func newTestInstaller(t *testing.T) (*Installer, *fakeExec, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	exec := newFakeExec()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in := &Installer{
		Layout: Layout{Dir: t.TempDir()}, Exec: exec, Clock: sqlstoretest.NewClock(fixedStart),
		Out: out, SourceDir: source, Sisf: "/usr/local/bin/sisf",
		LookPath: func(name string) (string, error) { return "/usr/local/go/bin/" + name, nil },
	}
	return in, exec, out
}

// initOptions are options for a simulated install in the given mode, with
// the budget confirmed.
func initOptions(in *Installer, mode manifest.Mode) Options {
	return Options{
		Dir: in.Layout.Dir, Mode: mode, Sim: true,
		Repo:   manifest.Repository{Owner: "acme", Repo: "hello-app"},
		Budget: BudgetChoice{Confirmed: true, AlertThresholdMicros: 50_000_000},
	}
}

// production turns simulated options into those of a production install of
// acme/shop signing in at a real OpenID Connect provider.
func production(o *Options) {
	o.Sim, o.Repo = false, manifest.Repository{Owner: "acme", Repo: "shop"}
	o.GitHubApp = GitHubApp{InstallationID: 7, Login: "acme-factory[bot]"}
	o.Identity = Identity{
		ExternalURL: "https://factory.example.com", Owners: []string{"https://accounts.example.com#owner-1"},
		OIDCIssuer: "https://accounts.example.com", OIDCClientID: "sisf-factory",
		OIDCClientSecret: core.NewSecretValue([]byte("oidc-client-secret-for-tests")),
	}
}

// mustInit runs Init and fails the test on an error.
func mustInit(t *testing.T, in *Installer, mode manifest.Mode) InitResult {
	t.Helper()
	res, err := in.Init(t.Context(), initOptions(in, mode))
	if err != nil {
		t.Fatalf("Init(%s): %v", mode, err)
	}
	return res
}

// containsAny reports whether text holds any of the values.
func containsAny(text string, values ...string) (string, bool) {
	for _, v := range values {
		if v != "" && strings.Contains(text, v) {
			return v, true
		}
	}
	return "", false
}

// discard keeps io imported for tests that silence output.
var _ io.Writer = io.Discard

// asCoreError unwraps err into target.
func asCoreError(err error, target **core.Error) bool { return errors.As(err, target) }
