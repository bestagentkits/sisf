package supplychain

import (
	"path"
	"slices"
	"strings"
)

// ChangeKind classifies a supply-chain change.
type ChangeKind string

const (
	// KindDependency is a dependency manifest such as go.mod.
	KindDependency ChangeKind = "dependency"
	// KindLockfile is a lockfile such as go.sum.
	KindLockfile ChangeKind = "lockfile"
	// KindBaseImage is a Dockerfile or Containerfile.
	KindBaseImage ChangeKind = "base_image"
	// KindCIConfig is continuous-integration configuration.
	KindCIConfig ChangeKind = "ci_config"
)

// Change is one changed path that touches the supply chain.
type Change struct {
	Path string     `json:"path"`
	Kind ChangeKind `json:"kind"`
}

// Dependency manifests and lockfiles by file name. Go files have pin
// checks; the others are recognised so a change to them is never missed,
// and the gate fails closed on them.
var (
	dependencyFiles = []string{
		"go.mod", "go.work", "package.json", "requirements.txt", "pyproject.toml", "Pipfile", "setup.py",
		"Cargo.toml", "Gemfile", "pom.xml", "build.gradle", "build.gradle.kts", "composer.json",
	}
	lockFiles = []string{
		"go.sum", "go.work.sum", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml",
		"poetry.lock", "Pipfile.lock", "uv.lock", "Cargo.lock", "Gemfile.lock", "composer.lock", "gradle.lockfile",
	}
	ciPrefixes = []string{".github/workflows/", ".github/actions/", ".circleci/"}
	ciFiles    = []string{".gitlab-ci.yml", ".github/dependabot.yml", ".github/dependabot.yaml"}
)

// DetectChanges returns the changed paths that touch the supply chain, in
// path order. Paths are repository-relative with forward slashes.
func DetectChanges(paths []string) []Change {
	var out []Change
	for _, p := range slices.Compact(slices.Sorted(slices.Values(paths))) {
		p = strings.TrimPrefix(path.Clean(strings.TrimSpace(p)), "./")
		if kind, ok := classify(p); ok {
			out = append(out, Change{Path: p, Kind: kind})
		}
	}
	return out
}

// classify returns the kind of a supply-chain path.
func classify(p string) (ChangeKind, bool) {
	base := path.Base(p)
	lower := strings.ToLower(base)
	switch {
	case slices.Contains(dependencyFiles, base) || isRequirementsFile(lower):
		return KindDependency, true
	case slices.Contains(lockFiles, base):
		return KindLockfile, true
	case isContainerfile(lower):
		return KindBaseImage, true
	case slices.Contains(ciFiles, p) || hasAnyPrefix(p, ciPrefixes):
		return KindCIConfig, true
	}
	return "", false
}

// isRequirementsFile matches pip requirement files such as requirements-dev.txt.
func isRequirementsFile(lower string) bool {
	return strings.HasPrefix(lower, "requirements") && strings.HasSuffix(lower, ".txt")
}

// isContainerfile matches Dockerfile, Dockerfile.x, x.Dockerfile and Containerfile.
func isContainerfile(lower string) bool {
	for _, name := range []string{"dockerfile", "containerfile"} {
		if lower == name || strings.HasPrefix(lower, name+".") || strings.HasSuffix(lower, "."+name) {
			return true
		}
	}
	return false
}

func hasAnyPrefix(p string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// pinChecked reports whether the gate can verify the pins of a change.
func pinChecked(c Change) bool {
	switch c.Kind {
	case KindDependency:
		return path.Base(c.Path) == "go.mod"
	case KindLockfile:
		return path.Base(c.Path) == "go.sum"
	case KindBaseImage, KindCIConfig:
		return true
	}
	return false
}
