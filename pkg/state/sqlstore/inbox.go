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

// PendingDeliveries returns the deliveries not yet processed, in the order
// they were received; deliveries received at the same instant keep the order
// they were stored in.
func (t *tx) PendingDeliveries(ctx context.Context, limit int) ([]state.InboxEntry, error) {
	if err := t.reads(tableInbox); err != nil {
		return nil, err
	}
	query, args := limited(`SELECT source, delivery_id, event_type, payload, received_at, processed_at
		FROM inbox WHERE factory_id = ? AND processed_at = 0 ORDER BY received_at, id`,
		[]any{string(t.store.factory)}, limit)
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

// MarkDeliveryProcessed records that a delivery needs no more work. Marking
// it again is harmless; an unknown delivery is NOT_FOUND.
func (t *tx) MarkDeliveryProcessed(ctx context.Context, source, deliveryID string) error {
	if err := t.writes(tableInbox, canSelect|canUpdate); err != nil {
		return err
	}
	changed, err := t.q.exec(ctx, "mark delivery processed", `UPDATE inbox SET processed_at = ?
		WHERE factory_id = ? AND source = ? AND delivery_id = ? AND processed_at = 0`,
		toNanos(t.store.now()), string(t.store.factory), source, deliveryID)
	if err != nil || changed == 1 {
		return err
	}
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
