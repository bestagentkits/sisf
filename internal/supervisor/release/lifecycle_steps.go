package release

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

// actorID names the supervisor in the lifecycle journal and the audit log.
const actorID = "service:supervisor"

// holding is the issue lease the pipeline writes under and the record as it
// last wrote it.
type holding struct {
	lease  state.Lease
	record core.StateRecord
}

// take acquires the issue lease and reads the record in one transaction.
// While the run's own lease is live the issue is not the supervisor's yet
// and LEASE_HELD is returned.
func (p *Pipeline) take(ctx context.Context, issue core.IssueRef) (*holding, error) {
	var h holding
	err := p.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		if h.lease, err = tx.AcquireLease(ctx, state.IssueLeaseResource(issue), p.cfg.LeaseOwner, p.cfg.LeaseTTL); err != nil {
			return err
		}
		h.record, err = tx.GetRecord(ctx, issue)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// renew extends the issue lease, so it outlasts the next window.
func (p *Pipeline) renew(ctx context.Context, h *holding) error {
	return p.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		lease, err := tx.RenewLease(ctx, h.lease, p.cfg.LeaseTTL)
		if err == nil {
			h.lease = lease
		}
		return err
	})
}

// letGo gives the issue lease up. A failure is harmless: the lease expires.
func (p *Pipeline) letGo(ctx context.Context, h *holding) {
	_ = p.cfg.State.Update(context.WithoutCancel(ctx), func(ctx context.Context, tx state.Tx) error {
		return tx.ReleaseLease(ctx, h.lease)
	})
}

// stepSpec is one lifecycle step the pipeline writes.
type stepSpec struct {
	event    core.Event
	reason   string
	evidence []string
	// candidate and release are named on the steps that must name them.
	candidate core.Digest
	release   core.ReleaseDigest
	guards    statemachine.Guards
	// also runs in the same transaction, after the step is written.
	also func(ctx context.Context, tx state.Tx, record core.StateRecord) error
}

// step renews the lease, has the lifecycle machine judge the step with its
// guards and writes it, all in one transaction, so a step is written only
// by the current holder of the issue and only when its proof holds.
func (p *Pipeline) step(ctx context.Context, h *holding, spec stepSpec) error {
	from := h.record
	to, err := statemachine.Next(from.State, spec.event)
	if err != nil {
		return err
	}
	return p.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		lease, err := tx.RenewLease(ctx, h.lease, p.cfg.LeaseTTL)
		if err != nil {
			return err
		}
		t := core.Transition{
			IssueID: from.IssueID, From: from.State, To: to, Event: spec.event,
			ExpectedVersion: from.Version, IdempotencyKey: "release/" + string(from.RunID) + "/" + string(spec.event),
			LeaseOwner: lease.Owner, FencingToken: lease.Token, RunID: from.RunID, Attempt: from.Attempt,
			ActorID: actorID, Reason: spec.reason, EvidenceRefs: spec.evidence,
			CandidateDigest: spec.candidate, ReleaseDigest: spec.release,
		}
		if err := statemachine.New(p.cfg.Clock, spec.guards).Check(ctx, t); err != nil {
			return err
		}
		record, err := tx.Transition(ctx, t)
		if err != nil {
			return err
		}
		if spec.also != nil {
			if err := spec.also(ctx, tx, record); err != nil {
				return err
			}
		}
		h.lease, h.record = lease, record
		return nil
	})
}

// audit records a release decision. The audit log is a transaction of its
// own and is written after the step; a failure to audit does not undo a
// step that happened, so it is returned for the caller to report.
func (p *Pipeline) audit(ctx context.Context, h *holding, action string, outcome core.AuditOutcome, details map[string]string) error {
	if p.cfg.Audit == nil {
		return nil
	}
	_, err := p.cfg.Audit.Append(ctx, core.AuditEvent{
		FactoryID: p.cfg.FactoryID, ActorID: actorID, ActorKind: core.ActorService, Action: action,
		Resource: "release", IssueID: h.record.IssueID, RunID: h.record.RunID, Outcome: outcome, Details: details,
	})
	return err
}
