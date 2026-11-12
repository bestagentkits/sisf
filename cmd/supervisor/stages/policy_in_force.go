package stages

import (
	"context"
	"encoding/json"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state"
)

// StoredBaseline reads the policy revision the Owner approved from the
// operational store, where the control plane records it.
type StoredBaseline struct{ State state.Store }

// Current returns the approved baseline, or NOT_FOUND before the first one.
func (b StoredBaseline) Current(ctx context.Context) (policy.Baseline, error) {
	var baseline policy.Baseline
	err := b.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, "policy", "baseline")
		if err != nil {
			return err
		}
		if err := json.Unmarshal(entry.Value, &baseline); err != nil || baseline.Revision.Validate() != nil {
			return core.NewError(core.CodeTampered, "the stored policy baseline cannot be read")
		}
		return nil
	})
	return baseline, err
}

// PolicyRefs returns the references of the policy in force for QA.
type PolicyRefs func(ctx context.Context) (core.PolicyRefs, error)

// QAPolicy reads the policy in force through loader and pins it for QA.
func QAPolicy(loader *policy.Loader) PolicyRefs { return stepPolicy(loader, core.StepQA) }

// stepPolicy reads the policy in force pinned for one step.
func stepPolicy(loader *policy.Loader, step core.StepKind) PolicyRefs {
	return func(ctx context.Context) (core.PolicyRefs, error) {
		snapshot, err := loader.Current(ctx)
		if err != nil {
			return core.PolicyRefs{}, err
		}
		return snapshot.Refs(step)
	}
}

// Baselines is the evaluator's read-only baseline: the approved policy
// commit, checked out in the evaluator's own workspace, with the suite the
// review criteria at that commit define. It changes only when the Owner
// approves another policy revision.
type Baselines struct {
	Workspace string
	Policy    PolicyRefs
}

// Baseline returns the baseline in force; the candidate plays no part in it.
func (b Baselines) Baseline(ctx context.Context, _ qa.Candidate) (qa.Baseline, error) {
	refs, err := b.Policy(ctx)
	if err != nil {
		return qa.Baseline{}, err
	}
	return qa.Baseline{
		CommitSHA: refs.PolicySHA, PolicySHA: refs.PolicySHA, SuiteDigest: handoff.SuiteDigest(refs),
		WorkspaceID: b.Workspace, ReadOnly: true,
	}, nil
}

// Ledger is the build ledger the builder writes in the agent zone's
// namespace; the evaluator reads it, never the candidate's own claim.
type Ledger struct{ State state.Store }

// FinishedBuild returns the digest of the finished build of commit.
func (l Ledger) FinishedBuild(ctx context.Context, commit core.CommitSHA) (core.Digest, error) {
	var digest core.Digest
	err := l.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		rec, err := handoff.ReadBuild(ctx, tx, commit)
		digest = rec.Digest
		return err
	})
	return digest, err
}
