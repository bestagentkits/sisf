package wiring

import (
	"context"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/actions/runactions"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// deliverQAHeld drains a hold notice an earlier release queued. A hold is
// shown in the issue's progress comment now, so no comment of its own is
// posted.
func (c *Control) deliverQAHeld(context.Context, state.OutboxMessage) error { return nil }

// storeQAHolds reads the supervisor's QA holds for runs.explain.
type storeQAHolds struct{ store state.Store }

func (s storeQAHolds) QAHold(ctx context.Context, run core.RunID, attempt int) (runactions.QAHold, error) {
	var h handoff.QAHold
	err := s.store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		h, err = handoff.ReadQAHold(ctx, tx, run, attempt)
		return err
	})
	if err != nil {
		return runactions.QAHold{}, err
	}
	return runactions.QAHold{AwaitsApproval: h.Cause == handoff.QAHoldOwnerApproval, Summary: h.Summary(),
		Ref: handoff.BuildNamespace + ":" + handoff.QAHoldKey(run, attempt), At: h.HeldAt}, nil
}

// storeCandidatePRs reads the candidates' pull requests for runs.inspect
// and runs.explain.
type storeCandidatePRs struct{ store state.Store }

func (s storeCandidatePRs) CandidatePR(ctx context.Context, run core.RunID) (runactions.CandidatePR, error) {
	var pr handoff.CandidatePR
	err := s.store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		pr, err = handoff.ReadCandidatePR(ctx, tx, run)
		return err
	})
	if err != nil {
		return runactions.CandidatePR{}, err
	}
	at := pr.DecidedAt
	if at.IsZero() {
		at = pr.OpenedAt
	}
	return runactions.CandidatePR{Number: pr.Number, URL: pr.URL, Branch: pr.Branch, State: string(pr.State), Reason: pr.Reason,
		MergeCommit: string(pr.MergeCommit), Ref: handoff.BuildNamespace + ":" + handoff.CandidatePRKey(run), At: at}, nil
}
