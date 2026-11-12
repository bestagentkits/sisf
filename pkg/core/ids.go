// Package core holds the contracts shared by every part of one factory
// deployment: identifiers, the issue lifecycle vocabulary, authority, budget,
// QA receipt, signing and audit types.
//
// It imports the standard library only, so any package may depend on it, and
// it contains no behavior beyond small pure helpers. Each type documents the
// invariant it protects.
package core

import (
	"regexp"
	"strconv"
	"strings"
)

// FactoryID names one factory deployment. All three zones, the release
// manifest and the install lifecycle share it; two factories on the same
// upstream repository never share one.
type FactoryID string

// DeploymentID names one installed instance of a factory on a platform. It is
// kept apart from FactoryID so that telemetry and labels can tell "which
// factory" from "which running install".
type DeploymentID string

// RunID names one execution attempt of an agent on an issue. It is a runtime
// identifier: it never doubles as a factory or deployment identity.
type RunID string

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

func validateID(kind, value string) error {
	if !idPattern.MatchString(value) {
		return invalid("%s %q must be 1-63 characters of [a-z0-9._-] starting with a letter or digit", kind, value)
	}
	return nil
}

// Validate reports whether the ID is safe to use in labels, paths and logs.
func (id FactoryID) Validate() error { return validateID("factory_id", string(id)) }

// Validate reports whether the ID is safe to use in labels, paths and logs.
func (id DeploymentID) Validate() error { return validateID("deployment_id", string(id)) }

// Validate reports whether the ID is safe to use in labels, paths and logs.
func (id RunID) Validate() error { return validateID("run_id", string(id)) }

// IssueRef identifies one GitHub issue. Operational records always point back
// to the issue they derive from; the issue itself stays authoritative in GitHub.
type IssueRef struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

var (
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)
	repoPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// String renders the reference as "owner/repo#number".
func (r IssueRef) String() string {
	return r.Owner + "/" + r.Repo + "#" + strconv.Itoa(r.Number)
}

// IsZero reports whether the reference is unset.
func (r IssueRef) IsZero() bool { return r == IssueRef{} }

// Validate rejects references that could not name a real issue.
func (r IssueRef) Validate() error {
	if !ownerPattern.MatchString(r.Owner) || !repoPattern.MatchString(r.Repo) || r.Number < 1 {
		return invalid("issue ref %q must be owner/repo#number with a positive number", r.String())
	}
	return nil
}

// ParseIssueRef parses the "owner/repo#number" form produced by String.
func ParseIssueRef(text string) (IssueRef, error) {
	repoPart, numberPart, ok := strings.Cut(text, "#")
	owner, repo, okRepo := strings.Cut(repoPart, "/")
	number, err := strconv.Atoi(numberPart)
	if !ok || !okRepo || err != nil {
		return IssueRef{}, invalid("issue ref %q must be owner/repo#number", text)
	}
	ref := IssueRef{Owner: owner, Repo: repo, Number: number}
	if err := ref.Validate(); err != nil {
		return IssueRef{}, err
	}
	return ref, nil
}

// Digest is an algorithm-prefixed content hash such as "sha256:<64 hex>". It
// binds receipts, approvals and artifacts to exact bytes; a mutable name such
// as a tag or branch is never a digest.
type Digest string

var digestPattern = regexp.MustCompile(`^(sha256:[a-f0-9]{64}|sha512:[a-f0-9]{128})$`)

// Validate rejects anything that is not a full-length lowercase digest.
func (d Digest) Validate() error {
	if !digestPattern.MatchString(string(d)) {
		return invalid("digest %q must be sha256:<64 hex> or sha512:<128 hex>", string(d))
	}
	return nil
}

// ReleaseDigest is the digest of an immutable release artifact. A release is
// identified by what was built, never by the API call that deployed it.
type ReleaseDigest string

// Validate rejects anything that is not a full-length lowercase digest.
func (d ReleaseDigest) Validate() error { return Digest(d).Validate() }

// CommitSHA is a full Git commit hash. Policy, baselines and candidates are
// pinned to a commit, never to a branch or tag that can move.
type CommitSHA string

var commitPattern = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)

// Validate rejects abbreviated hashes and symbolic refs.
func (s CommitSHA) Validate() error {
	if !commitPattern.MatchString(string(s)) {
		return invalid("commit %q must be a full 40- or 64-character lowercase hash", string(s))
	}
	return nil
}

// Zone is one of the three zones of a factory deployment. The zones may run
// as separate processes but never as separate deployments.
type Zone string

const (
	// ZoneApplication is Zone 1, the user-facing application.
	ZoneApplication Zone = "application"
	// ZoneAgent is Zone 2, the agent factory.
	ZoneAgent Zone = "agent"
	// ZoneControl is Zone 3, the control plane.
	ZoneControl Zone = "control"
	// ZoneShared places infrastructure used by all zones (state, artifacts,
	// edge). It is a placement, not a fourth zone.
	ZoneShared Zone = "shared"
)

// Zones returns the three zones in order. ZoneShared is deliberately absent.
func Zones() []Zone { return []Zone{ZoneApplication, ZoneAgent, ZoneControl} }

// Number returns 1, 2 or 3 for a zone and 0 for anything else.
func (z Zone) Number() int {
	switch z {
	case ZoneApplication:
		return 1
	case ZoneAgent:
		return 2
	case ZoneControl:
		return 3
	}
	return 0
}

// Shared reports whether the value is the shared placement.
func (z Zone) Shared() bool { return z == ZoneShared }

// Valid reports whether the value is a zone or the shared placement.
func (z Zone) Valid() bool { return z.Number() != 0 || z.Shared() }
