package stages

import (
	"path"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/internal/policy"
)

// ProtectedKind says why an agent's patch may not touch a path.
type ProtectedKind string

const (
	// ProtectedCI is anything under .github/: workflows, composite actions
	// and every other file GitHub or CI reads from there. A workflow pushed
	// to a candidate branch could run with the repository's secrets.
	ProtectedCI ProtectedKind = "ci"
	// ProtectedOwnership is a CODEOWNERS file at one of the locations
	// GitHub reads it from.
	ProtectedOwnership ProtectedKind = "code_ownership"
	// ProtectedPolicy is a foundation file of the factory: .sisf/ and the
	// decision rules, review criteria, execution guidance and feature
	// catalogue at the repository root.
	ProtectedPolicy ProtectedKind = "policy"
	// ProtectedMigration is a schema migration.
	ProtectedMigration ProtectedKind = "migration"
)

// codeOwnersLocations are where GitHub reads a CODEOWNERS file from.
var codeOwnersLocations = []string{"codeowners", ".github/codeowners", "docs/codeowners"}

// policyFiles are the foundation files at the repository root.
var policyFiles = []string{
	strings.ToLower(policy.PathAgents), strings.ToLower(policy.PathReview), strings.ToLower(policy.PathDecisions),
	strings.ToLower(policy.PathFeatures), strings.ToLower(policy.PathFeaturesYAML),
}

// policyDir is the directory of the factory's own configuration.
var policyDir = strings.ToLower(path.Dir(policy.PathFactory))

// ProtectedPath reports whether an agent's patch may not change the
// repository path p, and why. It is the one definition both the builder,
// before anything is committed or pushed, and the QA hard gates use. Paths
// are compared without regard to letter case, so a case-insensitive file
// system or checkout cannot be used to slip past an entry.
func ProtectedPath(p string) (ProtectedKind, bool) {
	clean := strings.ToLower(path.Clean("/" + strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")))[1:]
	switch {
	case clean == "":
		return "", false
	case slices.Contains(codeOwnersLocations, clean):
		return ProtectedOwnership, true
	case clean == ".github" || strings.HasPrefix(clean, ".github/"):
		return ProtectedCI, true
	case clean == policyDir || strings.HasPrefix(clean, policyDir+"/") || slices.Contains(policyFiles, clean):
		return ProtectedPolicy, true
	case strings.HasPrefix(clean, "migrations/") || strings.Contains(clean, "/migrations/"):
		return ProtectedMigration, true
	}
	return "", false
}
