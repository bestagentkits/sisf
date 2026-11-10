package budget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
)

// NoticeKind says why the Owner is notified.
type NoticeKind string

const (
	// NoticeAlertThreshold means spending in the period passed the alert
	// threshold. Nothing is stopped.
	NoticeAlertThreshold NoticeKind = "alert_threshold"
	// NoticeRunSuspended means a run used more than it reserved and was paused.
	NoticeRunSuspended NoticeKind = "run_suspended"
)

// Notice is one message for the Owner.
type Notice struct {
	Kind      NoticeKind     `json:"kind"`
	FactoryID core.FactoryID `json:"factory_id"`
	RunID     core.RunID     `json:"run_id"`
	// LimitMicros is the threshold passed, or the amount the run reserved.
	LimitMicros core.Micros `json:"limit_micros"`
	// SpentMicros is the spending of the period, or of the run, at the time.
	SpentMicros core.Micros `json:"spent_micros"`
	At          time.Time   `json:"at"`
}

// Notifier delivers notices. A failure to notify never changes a budget
// decision, so its error is not acted on. The gate calls it inside the
// transaction of the caller; an implementation should hand the notice to an
// outbox or queue rather than do slow work.
type Notifier interface {
	Notify(ctx context.Context, n Notice) error
}

// Config is what a Gate is built from.
type Config struct {
	// Confirmations reads the install-time confirmation; nil means the
	// entry the installer writes.
	Confirmations dispatcher.ConfirmationSource
	// Clock is the source of time; nil means the wall clock.
	Clock core.Clock
	// Notifier receives alerts; nil means none are sent.
	Notifier Notifier
}

// Gate is the budget gate. Pass it to the dispatcher as its budget gate.
type Gate struct {
	confirmations dispatcher.ConfirmationSource
	clock         core.Clock
	notifier      Notifier
}

var _ core.BudgetGate = (*Gate)(nil)

// NewGate builds the gate.
func NewGate(cfg Config) *Gate {
	g := &Gate{confirmations: cfg.Confirmations, clock: cfg.Clock, notifier: cfg.Notifier}
	if g.confirmations == nil {
		g.confirmations = dispatcher.KVConfirmation{}
	}
	if g.clock == nil {
		g.clock = core.SystemClock{}
	}
	return g
}

// Reserve admits a run. It refuses with BUDGET_UNCONFIRMED until the Owner
// confirmed the budget, and in capped mode with BUDGET_EXCEEDED when the
// reservation would reach into the rollback reserve or past the cap. A
// repeated idempotency key returns the first reservation.
func (g *Gate) Reserve(ctx context.Context, ledger core.BudgetLedger, req core.ReservationRequest) (core.Reservation, error) {
	return g.reserve(ctx, ledger, req, false)
}

// ReserveRollback admits a rollback run. It may spend the rollback reserve
// that Reserve keeps out of reach, though never more than the cap.
func (g *Gate) ReserveRollback(ctx context.Context, ledger core.BudgetLedger, req core.ReservationRequest) (core.Reservation, error) {
	return g.reserve(ctx, ledger, req, true)
}

func (g *Gate) reserve(ctx context.Context, ledger core.BudgetLedger, req core.ReservationRequest, rollback bool) (core.Reservation, error) {
	if err := req.Validate(); err != nil {
		return core.Reservation{}, err
	}
	if ledger == nil {
		return core.Reservation{}, core.NewError(core.CodeInvalidArgument, "a reservation needs the ledger of the claim transaction")
	}
	now := g.clock.Now()
	settings, err := effective(ctx, g.confirmations, ledger, now)
	if err != nil {
		return core.Reservation{}, err
	}
	id := reservationID(req.IdempotencyKey)
	existing, err := ledger.GetReservation(ctx, id)
	switch {
	case err == nil:
		if existing.IssueID != req.IssueID || existing.RunID != req.RunID {
			return core.Reservation{}, core.NewError(core.CodeConflict, "the idempotency key already reserved budget for another run")
		}
		return existing, nil
	case !core.HasCode(err, core.CodeNotFound):
		return core.Reservation{}, err
	}
	if settings.Mode == core.BudgetCapped {
		if err := g.fits(ctx, ledger, settings, req.Estimate, rollback, now); err != nil {
			return core.Reservation{}, err
		}
	}
	reservation := core.Reservation{
		ID: id, FactoryID: req.FactoryID, IssueID: req.IssueID, RunID: req.RunID,
		Mode: settings.Mode, Amount: req.Estimate, Status: core.ReservationHeld,
		// The factory's cap is only as hard as the provider's own limit.
		HardCapEnforced: settings.Mode == core.BudgetCapped && settings.ProviderHardCap,
		IdempotencyKey:  req.IdempotencyKey, CreatedAt: now,
	}
	if err := ledger.PutReservation(ctx, reservation); err != nil {
		return core.Reservation{}, err
	}
	return reservation, nil
}

// fits refuses an estimate that, added to what is held and spent in the
// period, would pass what the run may use.
func (g *Gate) fits(ctx context.Context, ledger core.BudgetLedger, s Settings, estimate core.Micros, rollback bool, now time.Time) error {
	if estimate <= 0 {
		return core.NewError(core.CodeInvalidArgument, "a capped budget needs an estimate above zero to reserve")
	}
	totals, err := ledger.BudgetTotals(ctx, s.Period.Start(now))
	if err != nil {
		return err
	}
	limit := s.CapMicros - s.RollbackReserveMicros
	if rollback {
		limit = s.CapMicros
	}
	if used := totals.Reserved + totals.SpentVerified + totals.SpentUnverified; used+estimate > limit {
		return core.NewError(core.CodeBudgetExceeded, "the reservation would pass the budget cap of the period",
			core.NextAction{Action: "budget.get", Description: "see what is spent and what remains"})
	}
	return nil
}

// reservationID derives the reservation of an idempotency key, so a retried
// claim finds the reservation it made before.
func reservationID(idempotencyKey string) string {
	sum := sha256.Sum256([]byte(idempotencyKey))
	return "res-" + hex.EncodeToString(sum[:16])
}
