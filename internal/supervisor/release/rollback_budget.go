package release

import (
	"context"
	"errors"
	"time"

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

// DefaultRollbackBudgetWait bounds each budget call around a rollback when
// the configuration names no other bound, so a slow or stuck store delays
// the restore by at most this much.
const DefaultRollbackBudgetWait = 2 * time.Second

// rollbackCharge is what one restore is charged to, and how a budget
// problem around it is audited.
type rollbackCharge struct {
	issue core.IssueRef
	run   core.RunID
	// key makes a retried restore reserve once.
	key   string
	audit func(ctx context.Context, outcome core.AuditOutcome, detail string)
}

// runRollbackCharge charges the automatic restore of the rollout's run.
func (p *Pipeline) runRollbackCharge(r *rollout) rollbackCharge {
	return rollbackCharge{issue: r.h.record.IssueID, run: r.h.record.RunID, key: rollbackIdempotencyKey(r.h.record.RunID),
		audit: func(ctx context.Context, outcome core.AuditOutcome, detail string) {
			_ = p.audit(ctx, r.h, "release.rollback_budget", outcome, map[string]string{"detail": detail})
		}}
}

// rollbackSpend is the budget side of one restore: the reservation it holds,
// if the gate admitted one.
type rollbackSpend struct {
	reservationID string
}

// reserveRollback sets the rollback's cost aside through the rollback
// reserve, waiting at most RollbackBudgetWait. A refusal, an error or a
// timeout never stops the restore: getting the LKG serving again outranks
// every budget, so the problem is audited and the restore goes on. A charge
// that names no run, such as an Owner rollback, cannot hold a reservation;
// its spend is recorded straight into the ledger instead.
func (p *Pipeline) reserveRollback(ctx context.Context, c rollbackCharge) rollbackSpend {
	if p.cfg.Budget == nil || c.issue.IsZero() || c.run == "" {
		return rollbackSpend{}
	}
	req := core.ReservationRequest{
		FactoryID: p.cfg.FactoryID, IssueID: c.issue, RunID: c.run, Estimate: p.cfg.RollbackCost, IdempotencyKey: c.key,
	}
	var reservation core.Reservation
	err := p.boundedBudgetUpdate(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		reservation, err = p.cfg.Budget.ReserveRollback(ctx, tx, req)
		return err
	})
	if err != nil {
		c.audit(ctx, core.OutcomeDenied, "the rollback runs without a reservation: "+string(core.CodeOf(err)))
		return rollbackSpend{}
	}
	return rollbackSpend{reservationID: reservation.ID}
}

// recordRollbackSpend writes what the restore spent, against its
// reservation when it has one and straight into the ledger otherwise, so no
// rollback goes unrecorded. The cost is the configured nominal cost and is
// unverified: no provider bills a restore.
func (p *Pipeline) recordRollbackSpend(ctx context.Context, c rollbackCharge, spend rollbackSpend) {
	if p.cfg.Budget == nil {
		return
	}
	usage := core.Usage{FactoryID: p.cfg.FactoryID, IssueID: c.issue, RunID: c.run, Cost: p.cfg.RollbackCost, At: p.cfg.Clock.Now().UTC()}
	err := p.boundedBudgetUpdate(ctx, func(ctx context.Context, tx state.Tx) error {
		if spend.reservationID != "" {
			return p.cfg.Budget.Settle(ctx, tx, spend.reservationID, usage)
		}
		return tx.AppendUsage(ctx, usage)
	})
	if err != nil {
		c.audit(ctx, core.OutcomeFailed, "the rollback's spend could not be recorded: "+string(core.CodeOf(err)))
	}
}

// boundedBudgetUpdate runs one budget transaction for at most
// RollbackBudgetWait. A timeout is TIMEOUT; the caller's own cancellation
// is kept as it is.
func (p *Pipeline) boundedBudgetUpdate(ctx context.Context, fn func(context.Context, state.Tx) error) error {
	bounded, cancel := context.WithTimeout(ctx, p.cfg.RollbackBudgetWait)
	defer cancel()
	err := p.cfg.State.Update(bounded, fn)
	if err != nil && ctx.Err() == nil && errors.Is(bounded.Err(), context.DeadlineExceeded) {
		return core.NewError(core.CodeTimeout, "the budget did not answer in time").WithCause(err)
	}
	return err
}

// rollbackIdempotencyKey makes a retried restore of a run reserve once.
func rollbackIdempotencyKey(run core.RunID) string { return "rollback/" + string(run) }
