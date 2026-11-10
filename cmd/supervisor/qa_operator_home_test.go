//go:build unix

package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	execlocal "github.com/bestagentkits/sisf/adapters/exec/local"
	"github.com/bestagentkits/sisf/pkg/core"
)

// localJudgeSetup is judgeSetup under --exec local and trusted_code_only,
// where the QA agent runs as the supervisor's own user.
func localJudgeSetup(t *testing.T) (*lifecycleFlags, judgeDeps) {
	t.Helper()
	local, err := execlocal.New(execlocal.Config{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	l, d := judgeSetup(t, nil)
	l.exec, d.docker, d.local = "local", nil, local
	d.manifest.TrustedCodeOnly = true
	return l, d
}

func TestTheQAAgentMayRunWithTheOperatorsHomeOnlyLocallyUnderTrustedCode(t *testing.T) {
	t.Parallel()
	l, d := localJudgeSetup(t)
	operator := t.TempDir()
	l.qa.home = operator
	root, home, tmp, err := l.qa.paths(d, l.exec)
	if err != nil || home != operator || tmp != filepath.Join(root, "home", "tmp") {
		t.Fatalf("an operator's home locally under trusted code: %q, %q, %v", home, tmp, err)
	}
	if _, err := l.judge(context.Background(), d); err != nil {
		t.Fatalf("the agent judge with the operator's home: %v", err)
	}

	// Under docker the launcher's sandbox owns the QA agent's home.
	l, d = judgeSetup(t, nil)
	l.qa.home = t.TempDir()
	if _, err := l.judge(context.Background(), d); !core.HasCode(err, core.CodePermissionDenied) {
		t.Errorf("an operator's home under docker: %v", err)
	}
	// Without trusted_code_only the agent may not run locally at all, and
	// the home is refused on its own as well.
	l, d = localJudgeSetup(t)
	d.manifest.TrustedCodeOnly = false
	l.qa.home = t.TempDir()
	if _, _, _, err := l.qa.paths(d, l.exec); !core.HasCode(err, core.CodePermissionDenied) {
		t.Errorf("an operator's home locally without trusted code: %v", err)
	}
}

func TestTheQAAgentsHomeLiesApartFromTheFactorysSecretsStateAndTrust(t *testing.T) {
	t.Parallel()
	install := t.TempDir()
	trust := filepath.Join(install, "data", "trust")
	state := filepath.Join(install, "data", "state")
	for _, dir := range []string{trust, state} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	linkToTrust := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(trust, linkToTrust); err != nil {
		t.Fatal(err)
	}
	l, _ := localJudgeSetup(t)
	l.factoryManifest = filepath.Join(install, "factory.yaml")
	protected := l.protectedDirs(trust, "STATE_DSN", func(n string) (string, bool) {
		return filepath.Join(state, "factory.db"), n == "STATE_DSN"
	})
	if !slices.Equal(protected, []string{trust, install, state}) {
		t.Fatalf("the protected directories are %v", protected)
	}
	for name, c := range map[string]struct {
		home string
		want core.ErrorCode
	}{
		"the trust volume":            {trust, core.CodePermissionDenied},
		"inside the trust volume":     {filepath.Join(trust, "home"), core.CodePermissionDenied},
		"a link to the trust volume":  {linkToTrust, core.CodePermissionDenied},
		"the state store's directory": {state, core.CodePermissionDenied},
		"the install directory":       {install, core.CodePermissionDenied},
		"a home holding the install":  {filepath.Dir(install), core.CodePermissionDenied},
		"a relative home":             {"relative/home", core.CodeInvalidArgument},
		"a home that does not exist":  {filepath.Join(t.TempDir(), "missing"), core.CodeInvalidArgument},
	} {
		l, d := localJudgeSetup(t)
		d.protected = protected
		l.qa.home = c.home
		if _, _, _, err := l.qa.paths(d, l.exec); !core.HasCode(err, c.want) {
			t.Errorf("%s: %v, want %s", name, err, c.want)
		}
	}
	// A server DSN names no local directory.
	if dirs := l.protectedDirs(trust, "STATE_DSN", func(string) (string, bool) { return "postgres://u:p@db/sisf", true }); dirs[2] != "" {
		t.Errorf("a server DSN gave the directory %q", dirs[2])
	}
}
