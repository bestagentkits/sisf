package release

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// DefaultRollbackCost is the nominal cost a rollback is reserved and
// recorded at when the configuration names none. A restore calls no model,
// so its real spend is the platform's; the nominal amount keeps every
// rollback visible in the ledger and lets a capped budget reserve it.
const DefaultRollbackCost core.Micros = 1

// RollbackBudget is the budget gate as the release pipeline uses it: a
// rollback reserves through the rollback reserve and settles what it spent.
// *budget.Gate is one.
type RollbackBudget interface {
	ReserveRollback(ctx context.Context, ledger core.BudgetLedger, req core.ReservationRequest) (core.Reservation, error)
	Settle(ctx context.Context, ledger core.BudgetLedger, reservationID string, actual core.Usage) error
}

// rollbackSpend is the budget side of one restore: the reservation it holds,
// if the gate admitted one.
type rollbackSpend struct {
	reservationID string
}

// reserveRollback sets the rollback's cost aside through the rollback
// reserve. A refusal never stops the restore: getting the LKG serving again
// outranks every budget, so the refusal is audited and the restore goes on.
func (p *Pipeline) reserveRollback(ctx context.Context, r *rollout) rollbackSpend {
	if p.cfg.Budget == nil {
		return rollbackSpend{}
	}
	req := core.ReservationRequest{
		FactoryID: p.cfg.FactoryID, IssueID: r.h.record.IssueID, RunID: r.h.record.RunID,
		Estimate: p.cfg.RollbackCost, IdempotencyKey: rollbackIdempotencyKey(r.h.record.RunID),
	}
	var reservation core.Reservation
	err := p.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		reservation, err = p.cfg.Budget.ReserveRollback(ctx, tx, req)
		return err
	})
	if err != nil {
		_ = p.audit(ctx, r.h, "release.rollback_budget", core.OutcomeDenied, map[string]string{
			"detail": "the rollback runs without a reservation: " + string(core.CodeOf(err)),
		})
		return rollbackSpend{}
	}
	return rollbackSpend{reservationID: reservation.ID}
}

// recordRollbackSpend writes what the restore spent, against its
// reservation when it has one and straight into the ledger when the gate
// refused one, so no rollback goes unrecorded. The cost is the configured
// nominal cost and is unverified: no provider bills a restore.
func (p *Pipeline) recordRollbackSpend(ctx context.Context, r *rollout, spend rollbackSpend) {
	if p.cfg.Budget == nil {
		return
	}
	usage := core.Usage{
		FactoryID: p.cfg.FactoryID, IssueID: r.h.record.IssueID, RunID: r.h.record.RunID,
		Cost: p.cfg.RollbackCost, At: p.cfg.Clock.Now().UTC(),
	}
	err := p.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		if spend.reservationID != "" {
			return p.cfg.Budget.Settle(ctx, tx, spend.reservationID, usage)
		}
		return tx.AppendUsage(ctx, usage)
	})
	if err != nil {
		_ = p.audit(ctx, r.h, "release.rollback_budget", core.OutcomeFailed, map[string]string{
			"detail": "the rollback's spend could not be recorded: " + string(core.CodeOf(err)),
		})
	}
}

// rollbackIdempotencyKey makes a retried restore of a run reserve once.
func rollbackIdempotencyKey(run core.RunID) string { return "rollback/" + string(run) }
