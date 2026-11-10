package sqlstore

import (
	"context"
	"database/sql"

	"github.com/bestagentkits/sisf/pkg/state"
)

// AcceptDelivery stores a delivery the first time its source and delivery ID
// are seen and reports false, storing nothing, every time after that. The
// content of a repeated delivery is ignored: the first one stands.
func (t *tx) AcceptDelivery(ctx context.Context, e state.InboxEntry) (bool, error) {
	if err := t.writes(tableInbox, canInsert); err != nil {
		return false, err
	}
	if err := requireText("delivery source", e.Source); err != nil {
		return false, err
	}
	if err := requireText("delivery ID", e.DeliveryID); err != nil {
		return false, err
	}
	changed, err := t.q.exec(ctx, "accept delivery", `INSERT INTO inbox
		(factory_id, source, delivery_id, event_type, payload, received_at, processed_at)
		VALUES (?, ?, ?, ?, ?, ?, 0)
		ON CONFLICT (factory_id, source, delivery_id) DO NOTHING`,
		string(t.store.factory), e.Source, e.DeliveryID, cleanText(e.EventType), bytesOrEmpty(e.Payload),
		toNanos(timeOr(e.ReceivedAt, t.store.now())))
	return changed == 1, err
}

// PendingDeliveries returns the deliveries not yet processed whose next
// attempt is due, in the order they were received; deliveries received at
// the same instant keep the order they were stored in. A delivery held back
// after a failure (see Store.DeferDelivery) is left out until its time, so
// deliveries that keep failing never take the places of those behind them.
func (t *tx) PendingDeliveries(ctx context.Context, limit int) ([]state.InboxEntry, error) {
	if err := t.reads(tableInbox); err != nil {
		return nil, err
	}
	query, args := limited(`SELECT source, delivery_id, event_type, payload, received_at, processed_at
		FROM inbox WHERE factory_id = ? AND processed_at = 0 AND next_attempt_at <= ? ORDER BY received_at, id`,
		[]any{string(t.store.factory), toNanos(t.store.now())}, limit)
	var entries []state.InboxEntry
	err := t.q.each(ctx, "list deliveries", query, args, func(rows *sql.Rows) error {
		var (
			e                   state.InboxEntry
			received, processed int64
		)
		if err := rows.Scan(&e.Source, &e.DeliveryID, &e.EventType, &e.Payload, &received, &processed); err != nil {
			return err
		}
		e.ReceivedAt, e.ProcessedAt = fromNanos(received), fromNanos(processed)
		entries = append(entries, e)
		return nil
	})
	return entries, err
}

// MarkDeliveryProcessed records that a delivery was handled and needs no
// more work. Marking it again is harmless; an unknown delivery is NOT_FOUND.
func (t *tx) MarkDeliveryProcessed(ctx context.Context, source, deliveryID string) error {
	return t.finishDelivery(ctx, source, deliveryID, DeliveryHandled, "")
}

// finishDelivery marks a delivery processed with how it left the inbox and
// why. A delivery already processed keeps what was recorded first.
func (t *tx) finishDelivery(ctx context.Context, source, deliveryID string, outcome DeliveryOutcome, reason string) error {
	if err := t.writes(tableInbox, canSelect|canUpdate); err != nil {
		return err
	}
	// A reason replaces the last failure; without one, the failure that
	// led here is kept.
	set, args := `processed_at = ?, outcome = ?`, []any{toNanos(t.store.now()), string(outcome)}
	if reason != "" {
		set, args = set+`, last_error = ?`, append(args, cleanText(reason))
	}
	changed, err := t.q.exec(ctx, "mark delivery processed", `UPDATE inbox SET `+set+`
		WHERE factory_id = ? AND source = ? AND delivery_id = ? AND processed_at = 0`,
		append(args, string(t.store.factory), source, deliveryID)...)
	if err != nil || changed == 1 {
		return err
	}
	return t.knownDelivery(ctx, source, deliveryID)
}

// knownDelivery returns NOT_FOUND unless the delivery was accepted.
func (t *tx) knownDelivery(ctx context.Context, source, deliveryID string) error {
	var known int64
	if err := t.q.row(ctx, `SELECT COUNT(*) FROM inbox WHERE factory_id = ? AND source = ? AND delivery_id = ?`,
		string(t.store.factory), source, deliveryID).Scan(&known); err != nil {
		return t.store.dialect.failure("read delivery", err)
	}
	if known == 0 {
		return notFound("delivery %s/%s was never accepted", source, deliveryID)
	}
	return nil
}
