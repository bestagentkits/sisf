package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/cmd/supervisor/runcontrol"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// paths returns the QA root and HOME, made private, refusing either when it
// is, holds or lies inside the trust volume.
func (q *qaFlags) paths(d judgeDeps) (root, home string, err error) {
	root, home = q.workspaceRoot, q.home
	if root == "" {
		root = filepath.Join(os.TempDir(), "sisf-qa-"+string(d.manifest.FactoryID))
	}
	if home == "" {
		home = filepath.Join(root, "home")
	}
	for _, dir := range []string{root, home} {
		if !filepath.IsAbs(dir) {
			return "", "", core.NewError(core.CodeInvalidArgument, "--qa-workspace-root and --qa-home must be absolute paths")
		}
		if d.trustDir != "" && overlaps(dir, d.trustDir) {
			return "", "", core.NewError(core.CodePermissionDenied, "the QA agent's directories must lie apart from the trust volume")
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", "", core.NewError(core.CodeUnavailable, "the QA agent's directory cannot be made").WithCause(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(home, "tmp"), 0o700); err != nil {
		return "", "", core.NewError(core.CodeUnavailable, "the QA agent's temporary directory cannot be made").WithCause(err)
	}
	return root, home, nil
}

// environment is the QA agent's whole environment: a PATH, its own HOME
// and temporary directory, and the credential variables it is allowed.
// The supervisor's own secrets are never among them.
func (q *qaFlags) environment(d judgeDeps, home string) ([]string, error) {
	path, ok := d.lookupEnv("PATH")
	if !ok || path == "" {
		path = defaultQAPath
	}
	env := []string{"PATH=" + path, "HOME=" + home, "TMPDIR=" + filepath.Join(home, "tmp")}
	names := slices.Clone(qaCredentialVars[q.agent])
	for name := range strings.SplitSeq(q.env, ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	forbidden := []string{"PATH", "HOME", "TMPDIR", runcontrol.EnvToken, envGitHubToken, envEdgeAdminToken, d.dsnEnv, d.rootKeyEnv}
	for _, name := range names {
		switch {
		case !envVarName.MatchString(name):
			return nil, core.NewError(core.CodeInvalidArgument, "--qa-agent-env names a variable that is not a variable name")
		case slices.Contains(forbidden, name) || hasAnyPrefix(strings.ToUpper(name), "SISF_", "EDGE_", "DOCKER_"):
			return nil, core.NewError(core.CodePermissionDenied, "the QA agent may not see the supervisor's variable "+name)
		}
		if value, set := d.lookupEnv(name); set && !slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, name+"=") }) {
			env = append(env, name+"="+value)
		}
	}
	return env, nil
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	return slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(s, p) })
}

// overlaps reports whether a and b are the same directory or one lies
// inside the other, after symbolic links are resolved.
func overlaps(a, b string) bool {
	a, b = resolved(a), resolved(b)
	inside := func(child, parent string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && (rel == "." || filepath.IsLocal(rel))
	}
	return inside(a, b) || inside(b, a)
}

// resolved resolves the symbolic links of the longest existing prefix of p.
func resolved(p string) string {
	p = filepath.Clean(p)
	var rest []string
	for dir := p; ; dir = filepath.Dir(dir) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(append([]string{real}, rest...)...)
		}
		if parent := filepath.Dir(dir); parent == dir {
			return p
		}
		rest = append([]string{filepath.Base(dir)}, rest...)
	}
}

// storeUsage records the QA agent's usage in the budget ledger of the
// operational store, as the control zone may.
type storeUsage struct{ store state.Store }

func (s storeUsage) AppendUsage(ctx context.Context, u core.Usage) error {
	return s.store.Update(ctx, func(ctx context.Context, tx state.Tx) error { return tx.AppendUsage(ctx, u) })
}
