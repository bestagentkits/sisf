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
	// never verified by the provider.
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

// CloseRunReservation ends the reservation of a run that will start no
// more coding attempts. The usage already booked stays in the ledger. When
// some of the run's work reported no cost, the Owner's estimate stands for
// it: the part of the reserved amount the reported cost does not cover is
// booked as an unverified estimate and the reservation is settled. Booking
// at most the amount keeps a capped reservation from being suspended by its
// own close. Otherwise the reservation is released. A reservation that is
// no longer held or suspended is left as it is.
func CloseRunReservation(ctx context.Context, gate ReservationCloser, ledger core.BudgetLedger, reservationID string, spend RunSpend) error {
	r, err := ledger.GetReservation(ctx, reservationID)
	if err != nil {
		return err
	}
	if r.Status != core.ReservationHeld && r.Status != core.ReservationSuspended {
		return nil
	}
	estimate := r.Amount - spend.ReportedCost
	if !spend.Unpriced || estimate <= 0 {
		return gate.Release(ctx, ledger, reservationID)
	}
	return gate.Settle(ctx, ledger, reservationID, core.Usage{IssueID: r.IssueID, RunID: r.RunID, Cost: estimate, Verified: false})
}
