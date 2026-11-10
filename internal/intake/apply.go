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
	// spend names the approval the step uses up. The step from
	// ApprovalPending to Ready is refused without one.
	spend *spending
	// retry is what is known about the request behind a step from Blocked
	// back to Ready: who asked, whether they may, and why and when the issue
	// was blocked. The scope proof of the retry is delegation or spend.
	retry *statemachine.RetryProof
	// supersedes is the approval request the step stops waiting on because
	// it now waits on another. It is closed once the step is written.
	supersedes string
}

// spending is the approval a step uses, and what is in force as it does.
type spending struct {
	requestID string
	// factory is the factory this intake serves, scope the digest of the
	// scope attached to the issue now, and revision the policy in force now.
	// The approval must have been given in that factory and cover both.
	factory  core.FactoryID
	scope    core.Digest
	revision core.CommitSHA
}

// guards returns the guards the lifecycle machine judges the step of the
// plan with. approval is the proof of the approval the step spends, or nil.
// Each guard hands over what the plan holds and nothing else, so a step for
// which the plan has no proof is refused.
func (p *plan) guards(approval *statemachine.ApprovalProof) statemachine.Guards {
	if p.retry != nil {
		proof := *p.retry
		proof.Approval, proof.Delegation = approval, p.delegation
		return statemachine.Guards{Retry: statemachine.RetryGuardFunc(
			func(context.Context, core.Transition) (statemachine.RetryProof, error) { return proof, nil })}
	}
	var guards statemachine.Guards
	if p.delegation != nil {
		guards.Delegation = statemachine.DelegationGuardFunc(
			func(context.Context, core.Transition) (statemachine.DelegationProof, error) {
				return *p.delegation, nil
			})
	}
	if approval != nil {
		guards.Approval = statemachine.ApprovalGuardFunc(
			func(context.Context, core.Transition) (statemachine.ApprovalProof, error) { return *approval, nil })
	}
	return guards
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
		if err := s.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error { return write(ctx, tx, p.guards(nil)) }); err != nil {
			return err
		}
		return s.supersede(ctx, p)
	}
	use := *p.spend
	_, err := s.cfg.Approvals.ConsumeWith(ctx, use.requestID, use.scope, use.revision,
		func(ctx context.Context, tx state.Tx, request approvals.Request, decision approvals.Approval) error {
			// The store hands over the request and the decision whose
			// signature it verified; the machine judges whether they
			// approve this scope, in this factory, under this policy.
			return write(ctx, tx, p.guards(&statemachine.ApprovalProof{Request: request, Approval: decision,
				FactoryID: use.factory, ScopeDigest: use.scope, PolicyRevision: use.revision}))
		})
	return err
}

// supersede closes the approval request a written step stopped waiting on,
// so the Owner can no longer decide it and nothing can spend it. It runs
// after the step is written: until then the issue still names that request.
// A failure is returned; the step stands, and the request merely stays open
// until it lapses, since nothing waits on it any more.
func (s *Service) supersede(ctx context.Context, p *plan) error {
	if p.supersedes == "" || p.supersedes == p.doc.ApprovalID {
		return nil
	}
	_, err := s.cfg.Approvals.Supersede(ctx, p.supersedes, p.doc.ApprovalID)
	return err
}
