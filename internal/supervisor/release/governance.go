package release

import (
	"path"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

//go:generate go run ../trustedclosure/gentrustedpackages -root ../../.. -out trusted_go_package_dirs_generated.go -package release

// supervisorUnit is the runtime unit of the supervisor itself.
const supervisorUnit = "supervisor"

// DefaultProtectedPaths returns the trusted control surface of the factory
// source: the supervisor, the signing keys, the supply-chain checks, the
// redaction of secrets, the agents' and the evaluator's images, policy
// enforcement, the rules of who may take which lifecycle step, the QA
// baseline, the rollback rules, the installer and CI. An entry ending in
// "/" covers a directory; any other entry is one file. A candidate that
// changes any of them is a governance change that needs a person, in a
// cycle of its own.
//
// Directories are listed rather than files where code in any file of a Go
// package can change what the package enforces: a new file in the state
// store with an init function could widen a zone's steps or append a
// migration as surely as an edit of the file that declares them.
//
// The Go part is not kept by hand: every directory of a package the trusted
// binaries link (trustedGoPackageDirs, generated from the dependency
// closure of the supervisor, the command line and the control unit) is
// protected, whatever it is for. That covers the installer, sign-in, roles
// and approvals, and the dashboard's templates and scripts, which show the
// Owner what an approval is for. The entries below are the rest: files
// that are not Go packages, and Go code that is trusted without being
// linked into a trusted binary (the synthetic suite, the edge).
func DefaultProtectedPaths() []string {
	return slices.Concat(handKeptProtectedPaths(), trustedGoPackageDirs)
}

// handKeptProtectedPaths are the protected paths the dependency closure of
// the trusted binaries does not name.
func handKeptProtectedPaths() []string {
	return []string{
		// The trusted release supervisor and how it is built and deployed.
		"cmd/supervisor/", "internal/supervisor/", "build/supervisor.Dockerfile", "templates/",
		// Signing keys and secrets, the supply-chain checks of what is
		// built and signed, and the redaction that keeps secrets and
		// personal data out of logs, evidence and the issue.
		"internal/signing/", "pkg/secrets/", "internal/secretbroker/", "internal/supplychain/", "internal/redact/",
		// The images the coding agents and the QA evaluator run in, which
		// fix what reaches the sandbox.
		"build/runner.Dockerfile", "build/qa-runner.Dockerfile",
		// Policy enforcement and the foundation files it reads.
		"internal/policy/", "pkg/core/", "internal/egress/", "internal/dockerfilter/", ".sisf/",
		"AGENTS.md", "REVIEW.md", "DECISIONS.md", "FEATURES.md", "features.yaml", "docs/adr/",
		// Which zone may take which lifecycle step and write which document,
		// and the schema migrations, some of whose PostgreSQL scripts are
		// built from those rules in Go: every zone binary links the store
		// and enforces them, and the database is migrated by it.
		"pkg/state/",
		// The QA baseline and evaluator, and the synthetic suite of the
		// application that judges a release whose real traffic is too low.
		"internal/qa/", "pkg/qa/",
		"examples/hello-app/synthetic/", "examples/hello-app/cmd/hello-app-synthetic/",
		// The rollback rules and the deployment contract they act through.
		"pkg/deploy/", "adapters/deploy/",
		// The CI that runs on the repository.
		".github/",
		// The edge, which decides which release receives traffic and is
		// what a rollback switches, and how it is built.
		"cmd/edge/", "build/edge.Dockerfile",
		// The module's dependencies: a changed version changes what every
		// trusted binary links without touching one of its directories.
		"go.mod", "go.sum",
	}
}

// checkSelfPublishable refuses a build of a unit the pipeline may not
// publish by itself. The rollout guard refuses it as well; refusing it here
// stages nothing.
func checkSelfPublishable(units []string, unit string) error {
	if slices.Contains(units, unit) {
		return nil
	}
	err := governance("unit %q is not one release.units lets the factory publish by itself", unit)
	err.Details = map[string]string{"unit": unit}
	return err
}

// checkGovernance refuses a candidate that is the supervisor, one whose
// changes are not listed (a change set nobody listed cannot be shown to
// leave the trusted surface alone), one that changes the trusted control
// surface, and one that changes any path that is not an input of the unit
// it releases (checkUnitInputs): the rule is default-deny, and the protected
// list is a second refusal on top of it. Paths are compared without regard
// to letter case, so a case-insensitive file system cannot be used to slip
// past an entry.
func checkGovernance(protected []string, inputs UnitInputs, unit string, changed []string) error {
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
	return checkUnitInputs(inputs, unit, changed)
}

// repoPath accepts a repository-relative path only in its own clean form:
// the builder commits the path exactly as it is spelled, so a path that
// trimming or cleaning would change (surrounding white space, a no-break
// space included, a "." segment, a doubled or trailing slash, a backslash)
// names another file than the one the rules would judge, and is refused.
// A path that is absolute or climbs out of the repository is refused too.
func repoPath(raw string) (string, bool) {
	if raw == "" || strings.ContainsRune(raw, '\\') || strings.HasPrefix(raw, "/") {
		return "", false
	}
	clean := path.Clean(strings.TrimSpace(raw))
	if clean != raw || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
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
