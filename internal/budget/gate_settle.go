package budget

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Settle records actual usage against a reservation. Usage is always
// appended, never dropped. In capped mode usage beyond the reserved amount
// suspends the reservation, and the run it belongs to is to be paused; the
// call itself succeeds so that the caller's transaction keeps the usage.
// Crossing the alert threshold notifies the Owner without stopping anything.
func (g *Gate) Settle(ctx context.Context, ledger core.BudgetLedger, reservationID string, actual core.Usage) error {
	reservation, err := ledger.GetReservation(ctx, reservationID)
	if err != nil {
		return err
	}
	if reservation.Status == core.ReservationReleased || reservation.Status == core.ReservationSettled {
		return core.NewError(core.CodeConflict, "the reservation is already "+string(reservation.Status))
	}
	if actual.IssueID != reservation.IssueID || actual.RunID != reservation.RunID {
		return core.NewError(core.CodeConflict, "the usage belongs to another issue or run than the reservation")
	}
	now := g.clock.Now()
	settings, err := effective(ctx, g.confirmations, ledger, now)
	if core.HasCode(err, core.CodeBudgetUnconfirmed) {
		// Spending that already happened is recorded whatever became of the
		// confirmation; the run keeps the mode it was admitted under.
		settings, err = Settings{Mode: reservation.Mode}, nil
	}
	if err != nil {
		return err
	}
	actual.FactoryID, actual.ReservationID = reservation.FactoryID, reservation.ID
	if actual.At.IsZero() {
		actual.At = now
	}
	if err := ledger.AppendUsage(ctx, actual); err != nil {
		return err
	}
	switch {
	case reservation.Status == core.ReservationSuspended:
		// Stays suspended until the run is released.
	case settings.Mode == core.BudgetCapped && actual.Cost > reservation.Amount:
		reservation.Status = core.ReservationSuspended
		g.notify(ctx, Notice{Kind: NoticeRunSuspended, FactoryID: reservation.FactoryID, RunID: reservation.RunID,
			LimitMicros: reservation.Amount, SpentMicros: actual.Cost, At: now})
	default:
		reservation.Status = core.ReservationSettled
	}
	if err := ledger.PutReservation(ctx, reservation); err != nil {
		return err
	}
	return g.alertOnCrossing(ctx, ledger, settings, reservation, actual)
}

// alertOnCrossing notifies once the usage just recorded carried the
// period's spending over the alert threshold.
func (g *Gate) alertOnCrossing(ctx context.Context, ledger core.BudgetLedger, s Settings, r core.Reservation, actual core.Usage) error {
	if s.AlertThresholdMicros <= 0 || actual.Cost <= 0 {
		return nil
	}
	totals, err := ledger.BudgetTotals(ctx, s.Period.Start(actual.At))
	if core.HasCode(err, core.CodePermissionDenied) {
		// A zone that may only append usage cannot total it, and the usage it
		// reports is already recorded, so it does not fail the settlement.
		// Whoever can read the ledger sees the crossing in budget.get.
		return nil
	}
	if err != nil {
		return err
	}
	spent := totals.SpentVerified + totals.SpentUnverified
	if spent >= s.AlertThresholdMicros && spent-actual.Cost < s.AlertThresholdMicros {
		g.notify(ctx, Notice{Kind: NoticeAlertThreshold, FactoryID: r.FactoryID, RunID: r.RunID,
			LimitMicros: s.AlertThresholdMicros, SpentMicros: spent, At: actual.At})
	}
	return nil
}

func (g *Gate) notify(ctx context.Context, n Notice) {
	if g.notifier != nil {
		_ = g.notifier.Notify(ctx, n) // a notice that cannot be delivered never changes a decision
	}
}

// Release frees a reservation whose run will spend nothing more. Usage
// already recorded stays in the ledger. A reservation that is no longer held
// or suspended is left as it is, so a settled run keeps its settlement.
func (g *Gate) Release(ctx context.Context, ledger core.BudgetLedger, reservationID string) error {
	reservation, err := ledger.GetReservation(ctx, reservationID)
	if err != nil {
		return err
	}
	if reservation.Status != core.ReservationHeld && reservation.Status != core.ReservationSuspended {
		return nil
	}
	reservation.Status = core.ReservationReleased
	return ledger.PutReservation(ctx, reservation)
}
