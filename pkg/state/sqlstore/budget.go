package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// reservationMoves lists, for each status of a reservation, the statuses it
// may move on to besides staying where it is. Settled and released are final,
// and a suspended run's money is never held again, so no rewrite of a
// reservation can set money aside anew that its run already gave up or
// spent, nor free it by going back. budgetRowRules holds the zone roles to
// the same list in PostgreSQL.
var reservationMoves = map[core.ReservationStatus][]core.ReservationStatus{
	core.ReservationHeld:      {core.ReservationSettled, core.ReservationSuspended, core.ReservationReleased},
	core.ReservationSuspended: {core.ReservationSettled, core.ReservationReleased},
}

// PutReservation makes a reservation, which is made held, or moves an
// existing one to another status along reservationMoves. The terms a
// reservation was made on never change: its issue, run and claim, which an
// ID that already belongs to another one is refused for with CONFLICT, so
// money set aside for one run cannot be handed to another; and its budget
// mode, amount and provider hard cap, so a rewrite cannot lower what the
// budget counts as set aside. A change of terms and a move the list does not
// have are refused with CONFLICT.
//
// Making a reservation needs the privilege to insert one and moving one the
// privilege to update one (zonePrivileges): the agent zone makes the
// reservation of its claim and moves none.
func (t *tx) PutReservation(ctx context.Context, r core.Reservation) error {
	if err := t.writes(tableReservations, canInsert); err != nil {
		return err
	}
	if err := t.ownFactory(&r.FactoryID); err != nil {
		return err
	}
	if err := requireText("reservation ID", r.ID); err != nil {
		return err
	}
	if err := r.IssueID.Validate(); err != nil {
		return err
	}
	if err := r.RunID.Validate(); err != nil {
		return err
	}
	switch {
	case !r.Mode.Valid():
		return invalid("unknown budget mode %q", string(r.Mode))
	case !r.Status.Valid():
		return invalid("unknown reservation status %q", string(r.Status))
	case r.Amount < 0:
		return invalid("reserved amount %d must not be negative", r.Amount)
	case r.IdempotencyKey == "":
		return invalid("a reservation needs the idempotency key of its claim")
	}
	stored, err := t.GetReservation(ctx, r.ID)
	if core.HasCode(err, core.CodeNotFound) {
		return t.makeReservation(ctx, r)
	}
	if err != nil {
		return err
	}
	if err := t.writes(tableReservations, canUpdate); err != nil {
		return err
	}
	switch {
	case stored.IssueID != r.IssueID || stored.RunID != r.RunID || stored.IdempotencyKey != r.IdempotencyKey:
		return conflict("reservation %s belongs to another run", r.ID)
	case stored.Mode != r.Mode || stored.Amount != r.Amount || stored.HardCapEnforced != r.HardCapEnforced:
		return conflict("reservation %s keeps the budget mode, amount and provider hard cap it was made with", r.ID)
	case stored.Status == r.Status:
		return nil
	case !slices.Contains(reservationMoves[stored.Status], r.Status):
		return conflict("reservation %s is %s and cannot become %s", r.ID, stored.Status, r.Status)
	}
	// The status read is part of the condition, so a concurrent move of the
	// same reservation makes this one change nothing and fail.
	changed, err := t.q.exec(ctx, "move reservation", `UPDATE reservations SET status = ?
		WHERE factory_id = ? AND id = ? AND status = ?`,
		string(r.Status), string(r.FactoryID), r.ID, string(stored.Status))
	if err != nil {
		return err
	}
	if changed != 1 {
		return conflict("reservation %s changed while it was being moved", r.ID)
	}
	return nil
}

// makeReservation inserts a reservation that does not exist yet. It is made
// held: a reservation that is settled, suspended or released from the start
// sets nothing aside and records no step of a run.
func (t *tx) makeReservation(ctx context.Context, r core.Reservation) error {
	if r.Status != core.ReservationHeld {
		return invalid("a reservation is made %s, not %s", core.ReservationHeld, r.Status)
	}
	// The agent zone's reservation is dated by the store: one dated long ago
	// would let rows dated before the budget period count for its run's
	// close (RunUsage counts a run's rows from its reservation's making).
	created := timeOr(r.CreatedAt, t.store.now())
	if t.store.zone == core.ZoneAgent {
		created = t.store.now()
	}
	changed, err := t.q.exec(ctx, "write reservation", `INSERT INTO reservations
		(factory_id, id, issue, run_id, budget_mode, amount, status, hard_cap_enforced, idempotency_key, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (factory_id, id) DO NOTHING`,
		string(r.FactoryID), r.ID, r.IssueID.String(), string(r.RunID), string(r.Mode), int64(r.Amount),
		string(r.Status), r.HardCapEnforced, r.IdempotencyKey, toNanos(created))
	if err != nil {
		return err
	}
	if changed != 1 {
		return conflict("reservation %s was made while it was being made", r.ID)
	}
	return nil
}

// GetReservation returns the reservation, or NOT_FOUND for an unknown ID.
func (t *tx) GetReservation(ctx context.Context, id string) (core.Reservation, error) {
	if err := t.reads(tableReservations); err != nil {
		return core.Reservation{}, err
	}
	var (
		issue, runID, mode, status string
		amount, createdAt          int64
	)
	r := core.Reservation{ID: id, FactoryID: t.store.factory}
	err := t.q.row(ctx, `SELECT issue, run_id, budget_mode, amount, status, hard_cap_enforced, idempotency_key, created_at
		FROM reservations WHERE factory_id = ? AND id = ?`, string(t.store.factory), id).
		Scan(&issue, &runID, &mode, &amount, &status, &r.HardCapEnforced, &r.IdempotencyKey, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Reservation{}, notFound("reservation %s does not exist", id)
	}
	if err == nil {
		r.IssueID, err = core.ParseIssueRef(issue)
	}
	if err != nil {
		return core.Reservation{}, t.store.dialect.failure("read reservation", err)
	}
	r.RunID, r.Mode, r.Status = core.RunID(runID), core.BudgetMode(mode), core.ReservationStatus(status)
	r.Amount, r.CreatedAt = core.Micros(amount), fromNanos(createdAt)
	return r, nil
}

// AppendUsage adds one metered cost to the ledger. Entries are never changed
// or removed afterwards. Usage that would lower the totals is refused, and
// so is usage booked as verified: nothing in the factory confirms a cost
// with its provider, so that claim could only be the writer's. A cost above
// core.MaxUsageCost is booked at that bound.
//
// The agent zone meters its own runs, so its rows are held closer: the
// store dates them with its own clock, whatever instant the writer gave,
// and books them only against an existing reservation of the run and issue
// the row names. The control zone's rows keep the instant given, or the
// store's clock without one.
func (t *tx) AppendUsage(ctx context.Context, u core.Usage) error {
	if err := t.writes(tableUsage, canInsert); err != nil {
		return err
	}
	if err := t.ownFactory(&u.FactoryID); err != nil {
		return err
	}
	issue := ""
	if !u.IssueID.IsZero() {
		if err := u.IssueID.Validate(); err != nil {
			return err
		}
		issue = u.IssueID.String()
	}
	if u.RunID != "" {
		if err := u.RunID.Validate(); err != nil {
			return err
		}
	}
	if u.Cost < 0 || u.InputTokens < 0 || u.OutputTokens < 0 {
		return invalid("usage must not be negative: cost %d, tokens %d in and %d out", u.Cost, u.InputTokens, u.OutputTokens)
	}
	if u.Verified {
		return invalid("usage cannot be booked as verified: nothing confirms a cost with its provider")
	}
	u.Cost = min(u.Cost, core.MaxUsageCost)
	at := timeOr(u.At, t.store.now())
	if t.store.zone == core.ZoneAgent {
		if err := t.agentBooksAgainstItsRun(ctx, u); err != nil {
			return err
		}
		at = t.store.now()
	}
	route, err := json.Marshal(u.ModelRoute)
	if err != nil {
		return invalid("the model route cannot be stored")
	}
	_, err = t.q.exec(ctx, "append usage", `INSERT INTO usage_ledger
		(factory_id, issue, run_id, reservation_id, step, model_route, input_tokens, output_tokens, cost, verified, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(u.FactoryID), issue, string(u.RunID), cleanText(u.ReservationID), cleanText(string(u.Step)), string(route),
		u.InputTokens, u.OutputTokens, int64(u.Cost), u.Verified, toNanos(at))
	return err
}

// agentBooksAgainstItsRun refuses an agent-zone row that names no run, or
// no reservation of that run and issue.
func (t *tx) agentBooksAgainstItsRun(ctx context.Context, u core.Usage) error {
	refused := func(why string) error {
		return core.NewError(core.CodePermissionDenied, "the agent zone books usage only against a reservation of its run: "+why)
	}
	if u.RunID == "" || u.ReservationID == "" {
		return refused("the usage names no run or no reservation")
	}
	r, err := t.GetReservation(ctx, u.ReservationID)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return refused("reservation " + cleanText(u.ReservationID) + " does not exist")
	case err != nil:
		return err
	case r.RunID != u.RunID || r.IssueID != u.IssueID:
		return refused("reservation " + r.ID + " belongs to another run or issue")
	}
	return nil
}

// BudgetTotals sums the reservations held now and the usage recorded at or
// after since. Usage the provider did not confirm is reported apart.
func (t *tx) BudgetTotals(ctx context.Context, since time.Time) (core.BudgetTotals, error) {
	if err := t.reads(tableReservations, tableUsage); err != nil {
		return core.BudgetTotals{}, err
	}
	var totals core.BudgetTotals
	var reserved int64
	if err := t.q.row(ctx, `SELECT `+t.store.dialect.cappedSum("amount")+` FROM reservations
		WHERE factory_id = ? AND status = ?`, string(t.store.factory), string(core.ReservationHeld)).Scan(&reserved); err != nil {
		return core.BudgetTotals{}, t.store.dialect.failure("sum reservations", err)
	}
	totals.Reserved = core.Micros(reserved)
	err := t.q.each(ctx, "sum usage", `SELECT verified, `+t.store.dialect.cappedSum("cost")+` FROM usage_ledger
		WHERE factory_id = ? AND recorded_at >= ? GROUP BY verified`,
		[]any{string(t.store.factory), toNanos(since)}, func(rows *sql.Rows) error {
			var (
				verified bool
				cost     int64
			)
			if err := rows.Scan(&verified, &cost); err != nil {
				return err
			}
			if verified {
				totals.SpentVerified = core.Micros(cost)
			} else {
				totals.SpentUnverified = core.Micros(cost)
			}
			return nil
		})
	if err != nil {
		return core.BudgetTotals{}, err
	}
	// Usage that states no cost, whatever tokens it counted
	// (core.Usage.CostUnreported), by the same rule RunUsage applies to one
	// run.
	if err := t.q.row(ctx, `SELECT CAST(COUNT(*) AS BIGINT) FROM usage_ledger
		WHERE factory_id = ? AND recorded_at >= ? AND verified = ? AND cost = 0`,
		string(t.store.factory), toNanos(since), false).Scan(&totals.Unpriced); err != nil {
		return core.BudgetTotals{}, t.store.dialect.failure("count usage without a cost", err)
	}
	return totals, nil
}

// RunUsage sums the usage of run in the ledger recorded at or after since.
// A store whose zone may not read the ledger refuses before any statement
// runs, so a PostgreSQL transaction is never aborted by the read, and the
// sum is capped rather than overflowing, for the same reason.
func (t *tx) RunUsage(ctx context.Context, run core.RunID, since time.Time) (core.RunUsage, error) {
	if err := t.reads(tableUsage); err != nil {
		return core.RunUsage{}, err
	}
	var (
		u              core.RunUsage
		cost           int64
		costUnreported int64
		negative       int64
	)
	// A row that states no cost leaves the run unpriced whatever tokens it
	// counted (core.Usage.CostUnreported).
	if err := t.q.row(ctx, `SELECT CAST(COUNT(*) AS BIGINT), `+t.store.dialect.cappedSum("cost")+`,
		CAST(COALESCE(SUM(CASE WHEN verified = ? AND cost = 0 THEN 1 ELSE 0 END), 0) AS BIGINT),
		CAST(COALESCE(SUM(CASE WHEN cost < 0 THEN 1 ELSE 0 END), 0) AS BIGINT)
		FROM usage_ledger WHERE factory_id = ? AND run_id = ? AND recorded_at >= ?`,
		false, string(t.store.factory), string(run), toNanos(since)).Scan(&u.Entries, &cost, &costUnreported, &negative); err != nil {
		return core.RunUsage{}, t.store.dialect.failure("sum the usage of a run", err)
	}
	u.Cost, u.CostUnreported, u.CostTampered = core.Micros(cost), costUnreported > 0, negative > 0
	return u, nil
}

// ownFactory fills in the factory of the store when the caller left it out
// and refuses a row that names another factory.
func (t *tx) ownFactory(id *core.FactoryID) error {
	if *id == "" {
		*id = t.store.factory
	}
	if *id != t.store.factory {
		return invalid("factory %q is not the factory of this store", string(*id))
	}
	return nil
}
