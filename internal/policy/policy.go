// Package policy loads the foundation files of a factory and decides which
// of them is in force.
//
// The foundation files are AGENTS.md, REVIEW.md, DECISIONS.md, the feature
// catalogue (FEATURES.md or features.yaml) and .sisf/factory.yaml. They are
// read only at the one commit the Owner approved, the baseline. A commit
// nobody approved is never read as policy, whoever wrote it and whether or
// not it was merged: until the Owner approves it, the old baseline stays in
// force.
//
// Sources are ranked, highest first: safety invariants in code and verified
// delegated authority; the Owner-approved configuration; the decision rules,
// review criteria, feature catalogue and ADRs; the execution guidance for
// agents; and last, issues, comments and logs, which are data and set
// nothing. A higher source overrides a lower one. Sources of equal rank that
// disagree, a file that is missing or malformed and a commit that is not the
// approved one all end in the same place: POLICY_HOLD. Nothing here picks
// between conflicting rules.
package policy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Paths of the foundation files, relative to the repository root.
const (
	PathAgents    = "AGENTS.md"
	PathReview    = "REVIEW.md"
	PathDecisions = "DECISIONS.md"
	// PathFeatures and PathFeaturesYAML are the two forms of the feature
	// catalogue. At least one must exist; when both do they must agree.
	PathFeatures     = "FEATURES.md"
	PathFeaturesYAML = "features.yaml"
	PathFactory      = ".sisf/factory.yaml"
)

// MaxFileSize is the largest foundation file accepted, in bytes.
const MaxFileSize = 1 << 20

// Source reads files of the repository at an exact commit. Git content is
// addressed by its hash, so what a commit holds never changes; a mutable
// name such as a branch is not accepted.
type Source interface {
	// ReadFile returns the content of path at revision, or NOT_FOUND when
	// the commit or the file does not exist.
	ReadFile(ctx context.Context, revision core.CommitSHA, path string) ([]byte, error)
}

// Document is one foundation file as it was read.
type Document struct {
	Path    string
	Content string
	// Digest is the hash of Content.
	Digest core.Digest
}

// newDocument wraps content read from path.
func newDocument(path string, content []byte) Document {
	return Document{Path: path, Content: string(content), Digest: digestOf(content)}
}

// digestOf returns the sha256 digest of data.
func digestOf(data []byte) core.Digest {
	sum := sha256.Sum256(data)
	return core.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

// Reasons a policy is on hold, carried in the "reason" detail of the error.
const (
	// reasonUnapproved: the commit is not the one the Owner approved, or
	// nothing was approved yet.
	reasonUnapproved = "unapproved"
	// reasonMissing: a foundation file does not exist at the commit.
	reasonMissing = "missing"
	// reasonInvalid: a foundation file cannot be read as what it must be.
	reasonInvalid = "invalid"
	// reasonConflict: sources of equal rank disagree.
	reasonConflict = "conflict"
	// reasonUnavailable: the repository or the store could not be read.
	reasonUnavailable = "unavailable"
)

// hold builds the POLICY_HOLD error. The reason is machine readable; the
// message says what a person has to fix.
func hold(reason, format string, args ...any) *core.Error {
	failure := core.NewError(core.CodePolicyHold, fmt.Sprintf(format, args...))
	failure.Details = map[string]string{"reason": reason}
	return failure
}
