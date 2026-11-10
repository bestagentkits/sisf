package agentenv

import (
	"os"
	"path/filepath"

	"github.com/bestagentkits/sisf/pkg/core"
)

// CheckOperatorHome checks a HOME the operator chose for an agent in place
// of its private one, such as the operator's own, where a subscription
// login of the agent's tool is kept. It must be an absolute, existing
// directory that neither is, holds, nor lies inside any of the protected
// directories (the factory's secrets, state and trust), after symbolic
// links are resolved. Empty protected entries are skipped.
//
// The caller decides whether an operator's HOME is allowed at all: only
// where the agent already runs as the operator's own user under the
// trusted_code_only model, never where a sandbox or launcher owns it.
func CheckOperatorHome(home string, protected ...string) error {
	if !filepath.IsAbs(home) {
		return core.NewError(core.CodeInvalidArgument, "the agent's home must be an absolute path")
	}
	for _, dir := range protected {
		if dir != "" && Overlaps(home, dir) {
			return core.NewError(core.CodePermissionDenied,
				"the agent's home must lie apart from the factory's secrets, state and trust directories")
		}
	}
	info, err := os.Stat(home)
	if err != nil || !info.IsDir() {
		return core.NewError(core.CodeInvalidArgument, "the agent's home is not an existing directory")
	}
	return nil
}

// Overlaps reports whether a and b are the same directory or one lies
// inside the other, after symbolic links are resolved.
func Overlaps(a, b string) bool {
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
