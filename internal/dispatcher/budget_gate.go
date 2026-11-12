package dispatcher

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Where the installer records the Owner's budget confirmation in the
// state store. The values match the install package, which writes the entry.
const (
	ConfirmationNamespace = "install"
	ConfirmationKey       = "budget_confirmation"
)

// BudgetConfirmation is the Owner's one-time confirmation of the budget
// mode at install, in the form the installer stores it. Until it exists the
// factory accepts intake but claims no work. With an unlimited budget the
// Owner must also have set an alert threshold or stated that no alert is
// wanted.
type BudgetConfirmation struct {
	Mode                 core.BudgetMode `json:"mode"`
	AlertThresholdMicros core.Micros     `json:"alert_threshold_micros,omitempty"`
	AlertOff             bool            `json:"alert_off,omitempty"`
	ConfirmedAt          time.Time       `json:"confirmed_at"`
}

// complete reports why the confirmation does not count, or "".
func (c BudgetConfirmation) complete(now time.Time) string {
	switch {
	case !c.Mode.Valid():
		return "the confirmation does not state a known budget mode"
	case c.ConfirmedAt.IsZero() || c.ConfirmedAt.After(now):
		return "the confirmation does not carry a past instant"
	case c.AlertThresholdMicros < 0:
		return "the alert threshold must not be negative"
	case c.Mode == core.BudgetUnlimited && c.AlertThresholdMicros == 0 && !c.AlertOff:
		return "an unlimited budget needs an alert threshold or an explicit choice of no alert"
	}
	return ""
}

// ConfirmationSource returns the install-time budget confirmation, or
// NOT_FOUND while there is none. The gate calls it inside the claim
// transaction and passes that transaction's ledger, so a source that reads
// the store reads through it; opening another transaction of the same store
// there is refused by the store.
type ConfirmationSource interface {
	BudgetConfirmation(ctx context.Context, ledger core.BudgetLedger) (BudgetConfirmation, error)
}

// ConfirmationFunc adapts a function to ConfirmationSource.
type ConfirmationFunc func(ctx context.Context, ledger core.BudgetLedger) (BudgetConfirmation, error)

// BudgetConfirmation calls f.
func (f ConfirmationFunc) BudgetConfirmation(ctx context.Context, ledger core.BudgetLedger) (BudgetConfirmation, error) {
	return f(ctx, ledger)
}

// KVConfirmation reads the confirmation the installer stored in the state
// store, through the claim transaction. Empty fields mean
// ConfirmationNamespace and ConfirmationKey.
type KVConfirmation struct {
	Namespace string
	Key       string
}

// BudgetConfirmation reads the entry. A missing entry is NOT_FOUND; one that
// cannot be read is TAMPERED, so a damaged confirmation never admits a run.
func (k KVConfirmation) BudgetConfirmation(ctx context.Context, ledger core.BudgetLedger) (BudgetConfirmation, error) {
	kv, ok := ledger.(state.KV)
	if !ok {
		return BudgetConfirmation{}, core.NewError(core.CodeUnavailable, "the claim transaction cannot read the budget confirmation")
	}
	namespace, key := cmp.Or(k.Namespace, ConfirmationNamespace), cmp.Or(k.Key, ConfirmationKey)
	entry, err := kv.GetKV(ctx, namespace, key)
	if err != nil {
		return BudgetConfirmation{}, err
	}
	var confirmation BudgetConfirmation
	if err := json.Unmarshal(entry.Value, &confirmation); err != nil {
		return BudgetConfirmation{}, core.NewError(core.CodeTampered, "the stored budget confirmation cannot be read")
	}
	return confirmation, nil
}

// ConfirmedBudgetGate admits runs only after the Owner confirmed the budget
// at install, and meters every admitted run through a reservation. It
// supports the unlimited mode, the default: a capped budget is refused with
// NOT_SUPPORTED rather than admitted without its cap being enforced.
type ConfirmedBudgetGate struct {
	confirmations ConfirmationSource
	clock         core.Clock
}

var _ core.BudgetGate = (*ConfirmedBudgetGate)(nil)

// NewConfirmedBudgetGate builds the gate. A nil clock means the wall clock.
func NewConfirmedBudgetGate(confirmations ConfirmationSource, clock core.Clock) (*ConfirmedBudgetGate, error) {
	if confirmations == nil {
		return nil, invalid("the budget gate needs a source of the budget confirmation")
	}
	if clock == nil {
		clock = core.SystemClock{}
	}
	return &ConfirmedBudgetGate{confirmations: confirmations, clock: clock}, nil
}

// Reserve admits a run. It refuses with BUDGET_UNCONFIRMED until a complete
// confirmation exists. A repeated idempotency key returns the reservation
// made the first time; reusing it for another run is a CONFLICT.
func (g *ConfirmedBudgetGate) Reserve(ctx context.Context, ledger core.BudgetLedger, req core.ReservationRequest) (core.Reservation, error) {
	if err := req.Validate(); err != nil {
		return core.Reservation{}, err
	}
	if ledger == nil {
		return core.Reservation{}, invalid("a reservation needs the ledger of the claim transaction")
	}
	now := g.clock.Now()
	confirmation, err := g.confirmations.BudgetConfirmation(ctx, ledger)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return core.Reservation{}, core.NewError(core.CodeBudgetUnconfirmed,
			"the Owner has not confirmed the budget mode yet, so no work is claimed").WithCause(err)
	case err != nil:
		return core.Reservation{}, err
	}
	if reason := confirmation.complete(now); reason != "" {
		return core.Reservation{}, core.NewError(core.CodeBudgetUnconfirmed, reason)
	}
	if confirmation.Mode != core.BudgetUnlimited {
		return core.Reservation{}, core.NewError(core.CodeNotSupported,
			"this budget gate cannot enforce a capped budget, so it admits no run under one")
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
	reservation := core.Reservation{
		ID: id, FactoryID: req.FactoryID, IssueID: req.IssueID, RunID: req.RunID,
		Mode: confirmation.Mode, Amount: req.Estimate, Status: core.ReservationHeld,
		// No monetary cap is set in unlimited mode, so none is enforced.
		HardCapEnforced: false, IdempotencyKey: req.IdempotencyKey, CreatedAt: now,
	}
	if err := ledger.PutReservation(ctx, reservation); err != nil {
		return core.Reservation{}, err
	}
	return reservation, nil
}

// Settle records actual usage against a reservation and settles it. Usage is
// appended even when it exceeds the amount set aside: with no cap there is
// nothing to suspend, and dropping it would hide spending.
func (g *ConfirmedBudgetGate) Settle(ctx context.Context, ledger core.BudgetLedger, reservationID string, actual core.Usage) error {
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
	actual.FactoryID, actual.ReservationID = reservation.FactoryID, reservation.ID
	if actual.At.IsZero() {
		actual.At = g.clock.Now()
	}
	if err := ledger.AppendUsage(ctx, actual); err != nil {
		return err
	}
	reservation.Status = core.ReservationSettled
	return ledger.PutReservation(ctx, reservation)
}

// Release frees a reservation whose run will spend nothing more. Releasing a
// reservation that is no longer held changes nothing.
func (g *ConfirmedBudgetGate) Release(ctx context.Context, ledger core.BudgetLedger, reservationID string) error {
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

// reservationID derives the reservation of an idempotency key, so a retried
// claim finds the reservation it made before.
func reservationID(idempotencyKey string) string {
	sum := sha256.Sum256([]byte(idempotencyKey))
	return "res-" + hex.EncodeToString(sum[:16])
}
