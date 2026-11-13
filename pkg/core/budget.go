package core

import (
	"context"
	"time"
)

// BudgetMode says whether the factory itself limits spending. Unlimited is
// the default and means only that the factory applies no monetary limit of
// its own. It never lifts the limits on time, attempts, concurrency or
// permissions, and it promises nothing about what a provider will charge.
type BudgetMode string

const (
	BudgetUnlimited BudgetMode = "unlimited"
	BudgetCapped    BudgetMode = "capped"
)

// Valid reports whether m is a known mode. The empty mode is invalid: the
// default is stated explicitly, never implied.
func (m BudgetMode) Valid() bool { return m == BudgetUnlimited || m == BudgetCapped }

// Micros is an amount of money in millionths of the ledger currency. Integer
// units keep ledger arithmetic exact.
type Micros int64

// MaxUsageCost bounds the cost of one usage entry: one million in the
// ledger currency. A cost an agent reports above it is booked at the bound,
// which is still more than any run reserves, so sums of the ledger stay far
// from overflowing and a run is never charged less for it.
const MaxUsageCost Micros = 1_000_000 * 1_000_000

// Usage is one metered piece of spending. Verified is false when the amount
// was estimated or the provider could not confirm it; such usage is shown as
// unverified and never presented as a guarantee. Nothing in the factory
// confirms a cost with its provider yet, so the store refuses usage booked
// as verified.
type Usage struct {
	FactoryID     FactoryID  `json:"factory_id"`
	IssueID       IssueRef   `json:"issue_id"`
	RunID         RunID      `json:"run_id"`
	ReservationID string     `json:"reservation_id,omitempty"`
	Step          StepKind   `json:"step,omitempty"`
	ModelRoute    ModelRoute `json:"model_route"`
	InputTokens   int64      `json:"input_tokens,omitempty"`
	OutputTokens  int64      `json:"output_tokens,omitempty"`
	Cost          Micros     `json:"cost"`
	Verified      bool       `json:"verified"`
	At            time.Time  `json:"at"`
}

// CostUnreported reports whether u is unverified and states no cost, as a
// CLI that prints tokens only does, whatever tokens it counted. Such usage
// is not free; its cost is unknown. A row that states no cost and no
// tokens is no proof that nothing was spent: the agent zone writes these
// rows, and an empty one must not stand for a run that spent nothing.
func (u Usage) CostUnreported() bool {
	return !u.Verified && u.Cost == 0
}

// ReservationStatus is the life stage of a reservation.
type ReservationStatus string

const (
	// ReservationHeld means the amount is set aside for a run in progress.
	ReservationHeld ReservationStatus = "held"
	// ReservationSettled means actual usage replaced the estimate.
	ReservationSettled ReservationStatus = "settled"
	// ReservationReleased means the run ended without using the amount.
	ReservationReleased ReservationStatus = "released"
	// ReservationSuspended means usage outran the estimate and the run is paused.
	ReservationSuspended ReservationStatus = "suspended"
)

// Valid reports whether s is a known status.
func (s ReservationStatus) Valid() bool {
	return s == ReservationHeld || s == ReservationSettled || s == ReservationReleased || s == ReservationSuspended
}

// ReservationRequest asks to set money aside before a run starts.
type ReservationRequest struct {
	FactoryID FactoryID `json:"factory_id"`
	IssueID   IssueRef  `json:"issue_id"`
	RunID     RunID     `json:"run_id"`
	// Estimate is the amount to set aside; zero is allowed in unlimited mode.
	Estimate Micros `json:"estimate"`
	// IdempotencyKey makes a retried claim reserve once.
	IdempotencyKey string `json:"idempotency_key"`
}

// Validate checks the structural rules of a request.
func (r ReservationRequest) Validate() error {
	if err := r.FactoryID.Validate(); err != nil {
		return err
	}
	if err := r.IssueID.Validate(); err != nil {
		return err
	}
	if err := r.RunID.Validate(); err != nil {
		return err
	}
	switch {
	case r.Estimate < 0:
		return invalid("estimate %d must not be negative", r.Estimate)
	case r.IdempotencyKey == "":
		return invalid("reservation needs an idempotency key")
	}
	return nil
}

// Reservation is money set aside for one run. A run is claimed only together
// with its reservation, so no work starts that the budget did not admit.
type Reservation struct {
	ID        string            `json:"id"`
	FactoryID FactoryID         `json:"factory_id"`
	IssueID   IssueRef          `json:"issue_id"`
	RunID     RunID             `json:"run_id"`
	Mode      BudgetMode        `json:"mode"`
	Amount    Micros            `json:"amount"`
	Status    ReservationStatus `json:"status"`
	// HardCapEnforced is false when the provider cannot enforce a limit, so
	// the level of guarantee is reported instead of overstated.
	HardCapEnforced bool      `json:"hard_cap_enforced"`
	IdempotencyKey  string    `json:"idempotency_key"`
	CreatedAt       time.Time `json:"created_at"`
}

// BudgetTotals summarizes the ledger since a point in time.
type BudgetTotals struct {
	Reserved        Micros `json:"reserved"`
	SpentVerified   Micros `json:"spent_verified"`
	SpentUnverified Micros `json:"spent_unverified"`
	// Unpriced counts the usage records whose provider reported no cost
	// (see Usage.CostUnreported). Their cost is not in the spending above.
	Unpriced int64 `json:"unpriced"`
}

// BudgetLedger is the durable record a gate works against. A state-store
// transaction satisfies it, which is how a reservation commits or rolls back
// together with the claim it belongs to. Usage entries are append-only.
type BudgetLedger interface {
	// PutReservation makes a reservation by ID, which must be held, or moves
	// the status of an existing one on: held to settled, suspended or
	// released, suspended to settled or released. The issue, run, idempotency
	// key, budget mode, amount and hard cap of a reservation never change,
	// writing the status it already has changes nothing, and any other change
	// is CONFLICT. A ledger that may only make reservations answers
	// PERMISSION_DENIED for an existing one.
	PutReservation(ctx context.Context, r Reservation) error
	// GetReservation returns NOT_FOUND for an unknown ID.
	GetReservation(ctx context.Context, id string) (Reservation, error)
	AppendUsage(ctx context.Context, u Usage) error
	// BudgetTotals sums reservations still held and usage recorded since the instant.
	BudgetTotals(ctx context.Context, since time.Time) (BudgetTotals, error)
	// RunUsage sums the usage of one run recorded at or after since. A
	// ledger the caller may append to but not read returns
	// PERMISSION_DENIED.
	RunUsage(ctx context.Context, run RunID, since time.Time) (RunUsage, error)
}

// RunUsage is what the ledger holds for one run since its reservation was
// made. A run's reservation is closed with it, never with the dispatcher's
// record of the run, which the agent zone can rewrite. It is still the agent
// zone's metering: the dispatcher books what its agents reported, so it can
// under-report what a run cost. What the store enforces is narrower: rows
// are append-only, an agent-zone row is dated by the store and booked
// against a reservation of its own run, and rows dated before the
// reservation do not count.
type RunUsage struct {
	// Cost sums the costs booked for the run, capped far above any amount a
	// run reserves rather than overflowing.
	Cost Micros `json:"cost"`
	// CostUnreported is set when some usage of the run stated no cost (see
	// Usage.CostUnreported).
	CostUnreported bool `json:"cost_unreported"`
	// Entries counts the usage records of the run.
	Entries int64 `json:"entries"`
	// CostTampered is set when some usage of the run has a negative cost,
	// which no store writes: such a row counts for nothing in Cost, and a
	// close charges the run as if its usage were unpriced.
	CostTampered bool `json:"cost_tampered"`
}

// BudgetGate decides whether a run may spend. It is consulted before a claim
// in every mode, so spending is always metered, and it refuses with
// BUDGET_UNCONFIRMED until the Owner has confirmed the budget mode and with
// BUDGET_EXCEEDED when a cap would be crossed. The ledger passed in is the
// transaction of the caller, so the decision and the claim are atomic.
type BudgetGate interface {
	// Reserve admits a run. A repeated idempotency key returns the first reservation.
	Reserve(ctx context.Context, ledger BudgetLedger, req ReservationRequest) (Reservation, error)
	// Settle records actual usage against a reservation; usage beyond the
	// amount suspends the reservation instead of being dropped.
	Settle(ctx context.Context, ledger BudgetLedger, reservationID string, actual Usage) error
	// Release frees a reservation whose run will spend nothing more.
	Release(ctx context.Context, ledger BudgetLedger, reservationID string) error
}
