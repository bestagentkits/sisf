package intake

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

// plan is one step intake decided on: at most one lifecycle transition,
// the document as it is afterwards, and what to show on GitHub.
type plan struct {
	transition *core.Transition
	doc        issueDoc
	// sow is a new scope version to store, when the step wrote one.
	sow     *SoW
	effects []state.OutboxMessage
	// delegation is the proof that the scope needs no approval. The step
	// from ScopeDraft to Ready is refused without it.
	delegation *statemachine.DelegationProof
	// spend names the approval the step from ApprovalPending to Ready uses
	// up. The step is refused without one.
	spend *spending
}

// spending is the approval a step uses, and what is in force as it does.
type spending struct {
	requestID string
	// scope is the digest of the scope attached to the issue now, and
	// revision the policy in force now. The approval must cover both.
	scope    core.Digest
	revision core.CommitSHA
}

// apply writes a step in one transaction: the transition, the documents
// and the queued effects commit together or not at all. A step that uses an
// approval spends it in that same transaction.
//
// Every transition is put before the lifecycle machine first. The machine
// judges the proof of a guarded step itself, so a plan cannot take an issue
// to Ready by claiming that it may.
func (s *Service) apply(ctx context.Context, snap snapshot, p *plan) error {
	write := func(ctx context.Context, tx state.Tx, guards statemachine.Guards) error {
		if p.transition != nil {
			if err := statemachine.New(s.cfg.Clock, guards).Check(ctx, *p.transition); err != nil {
				return err
			}
			if _, err := tx.Transition(ctx, *p.transition); err != nil {
				return err
			}
		}
		if p.sow != nil {
			if err := put(ctx, tx, sowKey(p.sow.Issue, p.sow.Version), 0, p.sow); err != nil {
				return err
			}
		}
		if err := put(ctx, tx, issueKey(p.doc.Issue), snap.docVersion, p.doc); err != nil {
			return err
		}
		for _, m := range p.effects {
			if err := tx.Enqueue(ctx, m); err != nil {
				return err
			}
		}
		return nil
	}
	if p.spend == nil {
		var guards statemachine.Guards
		if p.delegation != nil {
			guards.Delegation = statemachine.DelegationGuardFunc(func(context.Context, core.Transition) (statemachine.DelegationProof, error) {
				return *p.delegation, nil
			})
		}
		return s.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error { return write(ctx, tx, guards) })
	}
	use := *p.spend
	_, err := s.cfg.Approvals.ConsumeWith(ctx, use.requestID, use.scope, use.revision,
		func(ctx context.Context, tx state.Tx, request approvals.Request, decision approvals.Approval) error {
			// The store hands over the request and the decision whose
			// signature it verified; the machine judges whether they
			// approve this scope under this policy.
			proof := statemachine.ApprovalProof{Request: request, Approval: decision, ScopeDigest: use.scope, PolicyRevision: use.revision}
			return write(ctx, tx, statemachine.Guards{Approval: statemachine.ApprovalGuardFunc(
				func(context.Context, core.Transition) (statemachine.ApprovalProof, error) { return proof, nil })})
		})
	return err
}
