package main

import (
	"os"
	"path/filepath"
	"slices"

	"github.com/bestagentkits/sisf/cmd/supervisor/runcontrol"
	"github.com/bestagentkits/sisf/internal/manifestsettings"
	"github.com/bestagentkits/sisf/pkg/agent/agentenv"
	"github.com/bestagentkits/sisf/pkg/core"
)

// paths returns the QA root, HOME and TMPDIR. The root and the private
// HOME and TMPDIR below it are made private and must lie apart from the
// factory's secrets, state and trust. --qa-home replaces the HOME only
// where the agent already runs as the supervisor's user under
// trusted_code_only (--exec local); a launcher's sandbox owns the home
// of the agent it runs. TMPDIR stays private either way.
func (q *qaFlags) paths(d judgeDeps, exec string) (root, home, tmp string, err error) {
	root = q.workspaceRoot
	if root == "" {
		root = filepath.Join(os.TempDir(), "sisf-qa-"+string(d.manifest.FactoryID))
	}
	if !filepath.IsAbs(root) {
		return "", "", "", core.NewError(core.CodeInvalidArgument, "--qa-workspace-root and --qa-home must be absolute paths")
	}
	if slices.ContainsFunc(d.protected, func(p string) bool { return p != "" && agentenv.Overlaps(root, p) }) {
		return "", "", "", core.NewError(core.CodePermissionDenied, "the QA agent's directories must lie apart from the factory's secrets, state and trust")
	}
	home = filepath.Join(root, "home")
	tmp = filepath.Join(home, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return "", "", "", core.NewError(core.CodeUnavailable, "the QA agent's directories cannot be made").WithCause(err)
	}
	if q.home == "" {
		return root, home, tmp, nil
	}
	if exec != "local" || !d.manifest.TrustedCodeOnly {
		return "", "", "", core.NewError(core.CodePermissionDenied,
			"--qa-home is accepted only with --exec local under trusted_code_only; under --exec docker the launcher's sandbox owns the QA agent's home")
	}
	if err := agentenv.CheckOperatorHome(q.home, d.protected...); err != nil {
		return "", "", "", err
	}
	return root, q.home, tmp, nil
}

// environment is the QA agent's whole environment: a PATH, its own HOME
// and temporary directory, and the credential variables it is allowed.
// The supervisor's own secrets are never among them.
func (q *qaFlags) environment(d judgeDeps, home, tmp string) ([]string, error) {
	return agentenv.Build(agentenv.Spec{
		Agent: q.agent, Home: home, OperatorHome: q.home != "", TempDir: tmp, Extra: agentenv.Names(q.env), LookupEnv: d.lookupEnv,
		Forbidden: []string{runcontrol.EnvToken, envGitHubToken, envEdgeAdminToken, d.dsnEnv, d.rootKeyEnv},
	})
}

// protectedDirs are the factory's directories the supervisor knows of:
// the trust volume, the factory manifest's directory, which in a
// Standalone install is the install directory holding the secrets, and
// the SQLite state store's. The DSN is read only to find that directory.
func (l *lifecycleFlags) protectedDirs(trustDir, dsnEnv string, lookupEnv func(string) (string, bool)) []string {
	dirs := []string{trustDir}
	if l.factoryManifest != "" {
		if abs, err := filepath.Abs(l.factoryManifest); err == nil {
			dirs = append(dirs, filepath.Dir(abs))
		}
	}
	if dsn, ok := lookupEnv(dsnEnv); ok && dsnEnv != "" {
		dirs = append(dirs, manifestsettings.StateStoreDir(dsn))
	}
	return dirs
}
