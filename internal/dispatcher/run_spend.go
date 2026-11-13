package dispatcher

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// RunSpend is the tally of what a run's agents reported spending, kept
// with the run so that whoever ends the run can close its reservation.
// The usage itself is in the ledger, booked as each attempt ended.
type RunSpend struct {
	// ReportedCost sums the costs the agents stated; a tool's own figure,
	// never verified by the provider, kept in the agent zone's record. It
	// is shown, never used to close the run's reservation: that reads the
	// run's rows in the ledger (see CloseReservationOfRun).
	ReportedCost core.Micros `json:"reported_cost"`
	// Unpriced is set once work ran whose cost is unknown: its agent
	// reported no usage, or usage without a cost.
	Unpriced bool `json:"unpriced,omitempty"`
}

// Add counts usage an agent reported for one of the run's agent runs;
// reported is false when the agent could not say what the run consumed.
func (s *RunSpend) Add(usage []core.Usage, reported bool) {
	if !reported || len(usage) == 0 {
		s.Unpriced = true
	}
	for _, u := range usage {
		s.ReportedCost += u.Cost
		if u.Cost == 0 {
			s.Unpriced = true
		}
	}
}

// attemptUsage asks the agent what the current attempt's run consumed. An
// agent that cannot say is not an error: the attempt is then unpriced.
func (d *Dispatcher) attemptUsage(ctx context.Context, run RunRecord) ([]core.Usage, bool) {
	reporter, ok := d.cfg.Agent.(agent.UsageReporter)
	if !ok || run.Agent == nil {
		return nil, false
	}
	usage, err := reporter.Usage(ctx, *run.Agent)
	if err != nil {
		return nil, false
	}
	return usage, true
}

// bookAttempt appends what an attempt's agent reported to the ledger,
// against the run's reservation and as unverified, in the transaction that
// ends the attempt, and adds it to the run's tally.
func bookAttempt(ctx context.Context, tx state.Tx, run *RunRecord, usage []core.Usage, reported bool) error {
	for _, u := range usage {
		u.IssueID, u.RunID, u.ReservationID, u.Verified = run.Issue, run.RunID, run.ReservationID, false
		if u.Step == "" {
			u.Step = core.StepCoding
		}
		if err := tx.AppendUsage(ctx, u); err != nil {
			return err
		}
	}
	run.Spend.Add(usage, reported)
	return nil
}

// ReservationCloser settles or releases a reservation; core.BudgetGate is one.
type ReservationCloser interface {
	Settle(ctx context.Context, ledger core.BudgetLedger, reservationID string, actual core.Usage) error
	Release(ctx context.Context, ledger core.BudgetLedger, reservationID string) error
}

// CloseReservationOfRun ends the reservation of a run that will start no
// more coding attempts. The usage already booked stays in the ledger.
//
// What the run spent is read from the ledger rows of the run, never from
// the dispatcher's record of it: that record belongs to the agent zone,
// which can rewrite it, while the ledger is append-only, so whatever lowers
// the charge at the close is itself booked spending. When some of the run's
// work stated no cost, as the ledger shows or as unpricedClaimed says, the
// Owner's estimate stands for it: the part of the reserved amount the
// booked cost does not cover is booked as an unverified estimate and the
// reservation is settled. unpricedClaimed comes from the run's records and
// can only raise the charge: it covers an attempt whose agent reported
// nothing, which leaves no row. Booking at most the amount keeps a capped
// reservation from being suspended by its own close. Otherwise the
// reservation is released. A reservation of no amount is released without
// reading the ledger. A ledger the caller may not read counts as no booked
// cost with work unpriced, so the whole amount is charged as an unverified
// estimate rather than trusting the agent zone's figure; the close still
// succeeds. A reservation that is no longer held or suspended is left as
// it is.
//
// A reservation that belongs to another run than run is refused with
// TAMPERED and left as it is.
func CloseReservationOfRun(ctx context.Context, gate ReservationCloser, ledger core.BudgetLedger, run core.RunID, reservationID string, unpricedClaimed bool) error {
	if run == "" {
		return core.NewError(core.CodeInvalidArgument, "closing a reservation needs the run it belongs to")
	}
	r, err := ledger.GetReservation(ctx, reservationID)
	if err != nil {
		return err
	}
	if r.RunID != run {
		return core.NewError(core.CodeTampered, "reservation "+reservationID+" belongs to another run than "+string(run))
	}
	if r.Status != core.ReservationHeld && r.Status != core.ReservationSuspended {
		return nil
	}
	if r.Amount <= 0 {
		return gate.Release(ctx, ledger, reservationID)
	}
	booked, err := ledger.RunUsage(ctx, run)
	switch {
	case core.HasCode(err, core.CodePermissionDenied):
		booked = core.RunUsage{CostUnreported: true}
	case err != nil:
		return err
	}
	estimate := r.Amount - booked.Cost
	if (!unpricedClaimed && !booked.CostUnreported) || estimate <= 0 {
		return gate.Release(ctx, ledger, reservationID)
	}
	return gate.Settle(ctx, ledger, reservationID, core.Usage{IssueID: r.IssueID, RunID: r.RunID, Cost: estimate, Verified: false})
}
