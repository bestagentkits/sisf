package release

import (
	"path"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// supervisorUnit is the runtime unit of the supervisor itself.
const supervisorUnit = "supervisor"

// DefaultProtectedPaths returns the trusted control surface of the factory
// source: the supervisor, the signing keys, policy enforcement, the QA
// baseline and the rollback rules. An entry ending in "/" covers a
// directory; any other entry is one file. A candidate that changes any of
// them is a governance change that needs a person, in a cycle of its own.
func DefaultProtectedPaths() []string {
	return []string{
		// The trusted release supervisor and how it is built and deployed.
		"cmd/supervisor/", "internal/supervisor/", "build/supervisor.Dockerfile", "templates/",
		// Signing keys and secrets.
		"internal/signing/", "pkg/secrets/", "internal/secretbroker/",
		// Policy enforcement and the foundation files it reads.
		"internal/policy/", "pkg/core/", "internal/egress/", "internal/dockerfilter/", ".sisf/",
		"AGENTS.md", "REVIEW.md", "DECISIONS.md", "FEATURES.md", "features.yaml", "docs/adr/",
		// The QA baseline and evaluator.
		"internal/qa/", "pkg/qa/",
		// The rollback rules and the deployment contract they act through.
		"pkg/deploy/", "adapters/deploy/",
	}
}

// checkGovernance refuses a candidate that is the supervisor or changes the
// trusted control surface, and one whose changes are not listed: a change
// set nobody listed cannot be shown to leave that surface alone. Paths are
// compared without regard to letter case, so a case-insensitive file system
// cannot be used to slip past an entry.
func checkGovernance(protected []string, unit string, changed []string) error {
	if strings.EqualFold(strings.TrimSpace(unit), supervisorUnit) {
		return governance("the candidate is a build of the supervisor itself")
	}
	if len(changed) == 0 {
		return governance("the candidate does not list the paths it changes")
	}
	for _, raw := range changed {
		clean, ok := repoPath(raw)
		if !ok {
			return governance("the candidate changes %q, which is not a path inside the repository", raw)
		}
		for _, entry := range protected {
			if covers(entry, clean) {
				err := governance("the candidate changes %s, part of the trusted control surface", clean)
				err.Details = map[string]string{"path": clean, "protected": entry}
				return err
			}
		}
	}
	return nil
}

// repoPath cleans a repository-relative path. A path that is absolute or
// climbs out of the repository is refused.
func repoPath(raw string) (string, bool) {
	p := strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	if p == "" || strings.HasPrefix(p, "/") {
		return "", false
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

// covers reports whether a protected entry covers the cleaned path.
func covers(entry, clean string) bool {
	entry = strings.ToLower(entry)
	clean = strings.ToLower(clean)
	if dir, isDir := strings.CutSuffix(entry, "/"); isDir {
		return clean == dir || strings.HasPrefix(clean, dir+"/")
	}
	return clean == entry
}

// governance builds the refusal of a governance change.
func governance(format string, args ...any) *core.Error {
	err := invalid(format, args...)
	err.Code = core.CodeApprovalRequired
	err.Message += "; a governance change needs human approval in a separate update cycle"
	return err
}
