package release

import (
	"context"

	"github.com/bestagentkits/sisf/internal/catalog"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// FreezeCheck says whether an emergency stop froze publishing. An error is
// read as frozen.
type FreezeCheck interface {
	Frozen(ctx context.Context) (bool, error)
}

// Publisher writes the changelog and the feature catalog of a verified
// release as the Publisher identity and returns a reference to the change.
type Publisher interface {
	Publish(ctx context.Context, entry catalog.Entry) (string, error)
}

// SyntheticResult is one run of an application's synthetic suite.
type SyntheticResult struct {
	Passed      bool   `json:"passed"`
	EvidenceRef string `json:"evidence_ref,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

// SyntheticSuite runs the synthetic checks that cover the flows of the DoD
// against a revision. It is what a low-traffic release is judged by.
//
// Run returns a result for a run that judged the revision, passed or not,
// and an error for one that judged nothing: the suite could not be started
// or the caller's context ended.
type SyntheticSuite interface {
	Run(ctx context.Context, rev deploy.Revision) (SyntheticResult, error)
}

// SyntheticReadiness is what a synthetic suite may also offer: a check,
// without running it, that it can be run at all. A release that would be
// judged by the suite holds before anything is deployed when it cannot.
type SyntheticReadiness interface {
	Ready(ctx context.Context) error
}

// ArtifactObserver measures what the deployment backend would start for a
// release. ObserveArtifact returns the digest of those bytes, hashed by the
// supervisor at the call: NOT_FOUND when it holds nothing for the artifact,
// TAMPERED when what it holds cannot be what was received. It never returns
// a digest it did not measure: not the one the artifact carries, and not the
// one a release manifest gives for the installed release.
type ArtifactObserver interface {
	ObserveArtifact(ctx context.Context, a deploy.Artifact) (core.ReleaseDigest, error)
}
