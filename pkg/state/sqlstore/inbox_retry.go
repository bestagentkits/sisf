package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// DeliveryOutcome says how a delivery left the inbox.
type DeliveryOutcome string

const (
	// DeliveryHandled: the delivery was processed.
	DeliveryHandled DeliveryOutcome = "handled"
	// DeliveryRefused: the delivery can never be processed, such as one
	// that cannot be read or that was refused as malformed.
	DeliveryRefused DeliveryOutcome = "refused"
	// DeliveryGone: the issue or the repository it is about no longer
	// exists.
	DeliveryGone DeliveryOutcome = "gone"
	// DeliveryParked: the delivery kept failing for too long and is not
	// tried again. The last failure is kept as its reason.
	DeliveryParked DeliveryOutcome = "parked"
)

// Valid reports whether o is a known outcome.
func (o DeliveryOutcome) Valid() bool {
	switch o {
	case DeliveryHandled, DeliveryRefused, DeliveryGone, DeliveryParked:
		return true
	}
	return false
}

// FinishDelivery marks a delivery processed, recording how it left the inbox
// and, when reason is not empty, why. A delivery already processed keeps
// what was recorded first; an unknown delivery is NOT_FOUND.
func (s *Store) FinishDelivery(ctx context.Context, source, deliveryID string, outcome DeliveryOutcome, reason string) error {
	if !outcome.Valid() {
		return invalid("unknown delivery outcome %q", string(outcome))
	}
	return s.inTx(ctx, false, func(ctx context.Context, q *querier) error {
		t := &tx{store: s, q: q}
		return t.finishDelivery(ctx, source, deliveryID, outcome, reason)
	})
}

// DeferDelivery counts one failed attempt at a delivery, keeps reason as its
// last failure and holds it back for wait(failures), where failures counts
// this attempt. PendingDeliveries leaves it out until then. It returns the
// number of failures so far. A delivery already processed cannot fail any
// more: CONFLICT; an unknown delivery is NOT_FOUND.
func (s *Store) DeferDelivery(ctx context.Context, source, deliveryID, reason string, wait func(failures int) time.Duration) (int, error) {
	if wait == nil {
		return 0, invalid("deferring a delivery needs the wait after a failure")
	}
	var failures int
	err := s.inTx(ctx, false, func(ctx context.Context, q *querier) error {
		t := &tx{store: s, q: q}
		if err := t.writes(tableInbox, canSelect|canUpdate); err != nil {
			return err
		}
		// One statement counts the failure, so two passes that both failed
		// count two failures and neither is lost.
		var attempts int64
		err := q.row(ctx, `UPDATE inbox SET attempts = attempts + 1, last_error = ?
			WHERE factory_id = ? AND source = ? AND delivery_id = ? AND processed_at = 0
			RETURNING attempts`, cleanText(reason), string(s.factory), source, deliveryID).Scan(&attempts)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if err := t.knownDelivery(ctx, source, deliveryID); err != nil {
				return err
			}
			return conflict("delivery %s/%s was already processed", source, deliveryID)
		case err != nil:
			return s.dialect.failure("count a failed delivery", err)
		}
		failures = int(attempts)
		retryAt := s.now().Add(max(wait(failures), 0))
		_, err = q.exec(ctx, "defer delivery", `UPDATE inbox SET next_attempt_at = ?
			WHERE factory_id = ? AND source = ? AND delivery_id = ?`,
			toNanos(retryAt), string(s.factory), source, deliveryID)
		return err
	})
	if err != nil {
		return 0, err
	}
	return failures, nil
}

// DeliveryRecord is what the inbox remembers about one delivery's handling.
type DeliveryRecord struct {
	Source        string
	DeliveryID    string
	Attempts      int
	NextAttemptAt time.Time
	LastError     string
	// Outcome is empty while the delivery waits, and for a delivery
	// processed before outcomes were recorded.
	Outcome     DeliveryOutcome
	ProcessedAt time.Time
}

// Delivery returns how a delivery was handled so far, or NOT_FOUND.
func (s *Store) Delivery(ctx context.Context, source, deliveryID string) (DeliveryRecord, error) {
	record := DeliveryRecord{Source: source, DeliveryID: deliveryID}
	err := s.inTx(ctx, true, func(ctx context.Context, q *querier) error {
		t := &tx{store: s, q: q, readOnly: true}
		if err := t.reads(tableInbox); err != nil {
			return err
		}
		var attempts, next, processed int64
		var outcome string
		err := q.row(ctx, `SELECT attempts, next_attempt_at, last_error, outcome, processed_at FROM inbox
			WHERE factory_id = ? AND source = ? AND delivery_id = ?`, string(s.factory), source, deliveryID).
			Scan(&attempts, &next, &record.LastError, &outcome, &processed)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return notFound("delivery %s/%s was never accepted", source, deliveryID)
		case err != nil:
			return s.dialect.failure("read delivery", err)
		}
		record.Attempts, record.Outcome = int(attempts), DeliveryOutcome(outcome)
		record.NextAttemptAt, record.ProcessedAt = fromNanos(next), fromNanos(processed)
		return nil
	})
	if err != nil {
		return DeliveryRecord{}, err
	}
	return record, nil
}
