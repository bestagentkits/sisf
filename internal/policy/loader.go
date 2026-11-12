package policy

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/core"
)

// BaselineReader tells which policy revision the Owner approved.
type BaselineReader interface {
	// Current returns the approved baseline, or NOT_FOUND when the Owner
	// has approved nothing yet.
	Current(ctx context.Context) (Baseline, error)
}

// Loader hands out the policy in force. It reads the repository only at the
// approved baseline; every other commit is a hold.
type Loader struct {
	source    Source
	baselines BaselineReader
}

// NewLoader builds a loader over the repository and the baseline record.
func NewLoader(source Source, baselines BaselineReader) (*Loader, error) {
	if source == nil || baselines == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "a policy loader needs a source and the approved baseline")
	}
	return &Loader{source: source, baselines: baselines}, nil
}

// Current returns the policy in force: the foundation files at the commit
// the Owner approved. Commits made since, merged or not, play no part. It
// returns POLICY_HOLD when nothing is approved yet, when the files at the
// baseline cannot be read or contradict each other, or when their content is
// not the content that was approved.
func (l *Loader) Current(ctx context.Context) (*Snapshot, error) {
	baseline, err := l.approved(ctx)
	if err != nil {
		return nil, err
	}
	return l.load(ctx, baseline)
}

// At returns the policy at revision, which must be the approved baseline.
// Any other commit is POLICY_HOLD and is not read at all, so a caller that
// was handed a commit by an issue, a pull request or a model cannot make it
// policy by asking for it.
func (l *Loader) At(ctx context.Context, revision core.CommitSHA) (*Snapshot, error) {
	baseline, err := l.approved(ctx)
	if err != nil {
		return nil, err
	}
	if revision != baseline.Revision {
		failure := hold(reasonUnapproved, "commit %q is not the policy revision the Owner approved; %s stays in force",
			string(revision), baseline.Revision)
		failure.Details["approved"] = string(baseline.Revision)
		return nil, failure
	}
	return l.load(ctx, baseline)
}

// approved reads the baseline; having none is a hold, not an error to work
// around.
func (l *Loader) approved(ctx context.Context) (Baseline, error) {
	baseline, err := l.baselines.Current(ctx)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return Baseline{}, hold(reasonUnapproved, "the Owner has not approved a policy revision yet")
	case err != nil:
		return Baseline{}, hold(reasonUnavailable, "the approved policy revision could not be read").WithCause(err)
	}
	return baseline, nil
}

// load reads the files at the baseline and checks them against the digest
// the Owner approved. A source that returns other content for the same
// commit is wrong or compromised, and its content is not policy.
func (l *Loader) load(ctx context.Context, baseline Baseline) (*Snapshot, error) {
	snapshot, err := read(ctx, l.source, baseline.Revision)
	if err != nil {
		return nil, err
	}
	if snapshot.Digest != baseline.Digest {
		return nil, hold(reasonInvalid, "the files at %s are not the content the Owner approved", baseline.Revision)
	}
	return snapshot, nil
}
