package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"gopkg.in/yaml.v3"
)

// installerAt is a test installer for dir instead of a fresh directory.
func installerAt(t *testing.T, dir string) *Installer {
	t.Helper()
	in, _, _ := newTestInstaller(t)
	in.Layout = Layout{Dir: dir}
	return in
}

func TestAnInstallPathWithASpaceWorksEverywhere(t *testing.T) {
	t.Parallel()
	for _, mode := range []manifest.Mode{manifest.ModeStandalone, manifest.ModeDocker} {
		dir := filepath.Join(t.TempDir(), "my factory dir", "f1")
		in := installerAt(t, dir)
		if _, err := in.Init(t.Context(), initOptions(in, mode)); err != nil {
			t.Fatalf("%s: Init in a path with a space: %v", mode, err)
		}
		if mode == manifest.ModeDocker {
			// The Compose file is rendered at up; it must still parse, with
			// the install path whole in it.
			in.Exec.(*fakeExec).answer = buildAnswers
			if err := in.Up(t.Context(), UpOptions{Init: initOptions(in, mode)}); err != nil {
				t.Fatalf("Up of a Docker install in a path with a space: %v", err)
			}
			raw, err := os.ReadFile(in.Layout.Compose())
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := yaml.Unmarshal(raw, &doc); err != nil || doc["services"] == nil {
				t.Fatalf("the rendered Compose file does not parse: %v", err)
			}
			if !strings.Contains(string(raw), dir) {
				t.Errorf("the Compose file does not carry the install path %q whole", dir)
			}
			continue
		}
		env, err := readEnvFile(in.Layout.RuntimeEnv(areaControl))
		if err != nil || env["SISF_STATE_DSN"] != in.Layout.StateDB() {
			t.Fatalf("the state DSN read back = %q, %v; want %q", env["SISF_STATE_DSN"], err, in.Layout.StateDB())
		}
		unit, _ := os.ReadFile(filepath.Join(in.Layout.ServiceDir(), "sh.sisf.sisf-hello-app.service"))
		if !strings.Contains(string(unit), `--dir "`+dir+`"`) {
			t.Errorf("the systemd unit does not quote the install path:\n%s", unit)
		}
		// The SQLite store opens at that path.
		if err := os.MkdirAll(filepath.Dir(in.Layout.StateDB()), 0o700); err != nil {
			t.Fatal(err)
		}
		store, err := sqlstore.Open(context.Background(), sqlstore.Config{Dialect: sqlstore.DialectSQLite, DSN: in.Layout.StateDB(),
			FactoryID: "sisf-hello-app", Zone: core.ZoneControl, Clock: in.Clock})
		if err != nil {
			t.Fatalf("the state store does not open in a path with a space: %v", err)
		}
		_ = store.Close()
	}
	// What a value may never hold stays refused, in both kinds of env file.
	for _, bad := range []string{"a\"b", "a\nB=c", "a$b", "a#b", "a'b", "a\\b", "a  b", " a", "a "} {
		if _, err := formatLocalEnv("K", bad); err == nil {
			t.Errorf("formatLocalEnv accepted %q", bad)
		}
		if _, err := formatEnv("K", bad); err == nil {
			t.Errorf("formatEnv accepted %q", bad)
		}
	}
	if _, err := formatEnv("K", "/a b"); err == nil {
		t.Error("an env file Compose reads accepted a space")
	}
	for _, bad := range []string{"/a\"b", "/a%b", "/a$b", "/a<b", "/a\\b", "/a  b", "/a\nb"} {
		if quotedPathPattern.MatchString(bad) {
			t.Errorf("a unit would carry %q", bad)
		}
	}
}

func TestAFailedInitLeavesNothingBehind(t *testing.T) {
	t.Parallel()
	// A directory init made is removed, with the parents it made.
	parent := t.TempDir()
	in := installerAt(t, filepath.Join(parent, "new parent", "f1"))
	in.Rand = strings.NewReader("") // fails after the directory is made
	if _, err := in.Init(t.Context(), initOptions(in, manifest.ModeStandalone)); err == nil {
		t.Fatal("Init succeeded without random bytes")
	}
	if entries, _ := os.ReadDir(parent); len(entries) != 0 {
		t.Fatalf("a failed init left %v in %s", entries, parent)
	}
	// A directory that existed keeps what it held, and only that.
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "credentials.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	in = installerAt(t, home)
	in.Rand = strings.NewReader("")
	if _, err := in.Init(t.Context(), initOptions(in, manifest.ModeStandalone)); err == nil {
		t.Fatal("Init succeeded without random bytes")
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 1 || entries[0].Name() != "credentials.json" {
		t.Fatalf("after a failed init the directory holds %v, want only credentials.json", entries)
	}
}

func TestInitRefusesADirectoryThatHoldsOtherFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := installerAt(t, dir)
	_, err := in.Init(t.Context(), initOptions(in, manifest.ModeStandalone))
	if !core.HasCode(err, core.CodeInvalidArgument) || !strings.Contains(err.Error(), "notes.txt") {
		t.Fatalf("Init into a directory with notes.txt = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("a refused init wrote into the directory: %v", entries)
	}
}

func TestARealInstallMustNameItsGitHubAppBeforeAnythingIsWritten(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	o := initOptions(in, manifest.ModeStandalone)
	production(&o)
	o.GitHubToken = core.NewSecretValue([]byte("token-for-tests"))
	o.GitHubApp = GitHubApp{}
	_, err := in.Init(t.Context(), o)
	if !core.HasCode(err, core.CodeInvalidArgument) || !strings.Contains(err.Error(), "--github-installation-id") || !strings.Contains(err.Error(), "--github-app-login") {
		t.Fatalf("Init without the GitHub App = %v", err)
	}
	if entries, _ := os.ReadDir(in.Layout.Dir); len(entries) != 0 {
		t.Fatalf("a refused init wrote %v", entries)
	}
	o.GitHubApp = GitHubApp{InstallationID: 7, Login: "bad login!"}
	if _, err := in.Init(t.Context(), o); err == nil {
		t.Fatal("a login that is not a GitHub login was accepted")
	}
	o.GitHubApp = GitHubApp{InstallationID: 7, Login: "acme-factory[bot]", AppID: 42}
	if _, err := in.Init(t.Context(), o); err != nil {
		t.Fatalf("Init with the GitHub App: %v", err)
	}
	m, _ := in.Layout.LoadManifest()
	if err := checkStartable(m); err != nil {
		t.Fatalf("the written manifest would not start: %v", err)
	}
	sim, _, _ := newTestInstaller(t)
	so := initOptions(sim, manifest.ModeStandalone)
	so.GitHubApp = GitHubApp{InstallationID: 7}
	if _, err := sim.Init(t.Context(), so); err == nil {
		t.Fatal("the simulation accepted a GitHub App of its own")
	}
}

func TestServiceUnitsAreWrittenOnlyWhenTheyCanStartTheFactory(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		sisf string
		goOK bool
		want string
	}{
		"a go run build": {filepath.Join(os.TempDir(), "go-build123", "b001", "exe", "sisf"), true, "temporary `go run` build"},
		"no go":          {"/usr/local/bin/sisf", false, "`go` is not on PATH"},
	} {
		in, _, _ := newTestInstaller(t)
		in.Sisf = tc.sisf
		if !tc.goOK {
			in.LookPath = func(string) (string, error) { return "", errors.New("not found") }
		}
		res, err := in.Init(t.Context(), initOptions(in, manifest.ModeStandalone))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := os.Stat(in.Layout.ServiceDir()); err == nil {
			t.Errorf("%s: service units were written", name)
		}
		if !strings.Contains(strings.Join(res.Warnings, "\n"), tc.want) {
			t.Errorf("%s: warnings %v do not say %q", name, res.Warnings, tc.want)
		}
	}
	in, _, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeStandalone)
	if info, err := os.Stat(in.Layout.Logs()); err != nil || !info.IsDir() {
		t.Error("the log directory the launchd unit writes to was not created")
	}
	plist, _ := os.ReadFile(filepath.Join(in.Layout.ServiceDir(), "sh.sisf.sisf-hello-app.plist"))
	for _, want := range []string{"<string>--source</string>", "<key>WorkingDirectory</key><string>" + in.SourceDir, "<key>PATH</key>"} {
		if !strings.Contains(string(plist), want) {
			t.Errorf("the launchd unit lacks %q", want)
		}
	}
	if got, err := RecordedSource(in.Layout.Dir); err != nil || got != in.SourceDir {
		t.Errorf("RecordedSource = %q, %v; want %q", got, err, in.SourceDir)
	}
}

// writeSourceCheckout makes dir look like a checkout of this project to
// FindSource: its go.mod and the programs `sisf up` builds.
func writeSourceCheckout(t *testing.T, dir string) {
	t.Helper()
	for _, p := range standalonePrograms {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(p.pkg)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(sourceModule+"\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindSourceWalksUpToThisProjectsCheckout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSourceCheckout(t, root)
	sub := filepath.Join(root, "cmd", "sisf")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := FindSource(sub); got != root {
		t.Fatalf("FindSource = %q, want %q", got, root)
	}
	if got := FindSource(t.TempDir()); got != "" {
		t.Fatalf("FindSource outside a checkout = %q", got)
	}
}

// A go.mod naming this module is not a checkout by itself, such as a copy
// left in a parent directory: `sisf up` could build nothing from it.
func TestFindSourcePassesAStrayGoModOfThisModule(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSourceCheckout(t, root)
	stray := filepath.Join(root, "notes")
	work := filepath.Join(stray, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, "go.mod"), []byte(sourceModule+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := FindSource(work); got != root {
		t.Fatalf("FindSource = %q, want the checkout %q past the stray go.mod", got, root)
	}
	alone := t.TempDir()
	if err := os.WriteFile(filepath.Join(alone, "go.mod"), []byte(sourceModule+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := FindSource(alone); got != "" {
		t.Fatalf("FindSource of a lone go.mod = %q, want none", got)
	}
}

func TestInitNamesTheEntriesOfADirectoryThatIsNotEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, tc := range []struct{ name, want string }{
		{"notes.txt", "already holds 1 other entry (notes.txt)"},
		{"todo.txt", "already holds 2 other entries (notes.txt, todo.txt)"},
	} {
		if err := os.WriteFile(filepath.Join(dir, tc.name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		err := checkEmptyTarget(dir)
		if !core.HasCode(err, core.CodeInvalidArgument) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("checkEmptyTarget = %v, want %q", err, tc.want)
		}
	}
}
