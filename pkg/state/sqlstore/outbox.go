package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/bestagentkits/sisf/pkg/state"
)

// Enqueue adds a side effect to perform after commit. One message exists per
// key for the life of the store: a key that is queued or was already sent
// makes the call a no-op, so a retried decision does not repeat its effect.
func (t *tx) Enqueue(ctx context.Context, m state.OutboxMessage) error {
	if err := t.writes(tableOutbox, canInsert); err != nil {
		return err
	}
	if err := requireText("outbox topic", m.Topic); err != nil {
		return err
	}
	if err := requireText("outbox key", m.Key); err != nil {
		return err
	}
	_, err := t.q.exec(ctx, "enqueue message", `INSERT INTO outbox
		(factory_id, topic, msg_key, payload, attempts, next_attempt_at, last_error, created_at, sent_at)
		VALUES (?, ?, ?, ?, 0, ?, '', ?, 0)
		ON CONFLICT (factory_id, msg_key) DO NOTHING`,
		string(t.store.factory), m.Topic, m.Key, bytesOrEmpty(m.Payload),
		toNanos(m.NextAttemptAt), toNanos(timeOr(m.CreatedAt, t.store.now())))
	return err
}

// PendingOutbox returns the unsent messages whose next attempt is due, in
// the order they were queued.
func (t *tx) PendingOutbox(ctx context.Context, limit int) ([]state.OutboxMessage, error) {
	if err := t.reads(tableOutbox); err != nil {
		return nil, err
	}
	query, args := limited(`SELECT id, topic, msg_key, payload, attempts, next_attempt_at, last_error, created_at, sent_at
		FROM outbox WHERE factory_id = ? AND sent_at = 0 AND next_attempt_at <= ? ORDER BY id`,
		[]any{string(t.store.factory), toNanos(t.store.now())}, limit)
	var messages []state.OutboxMessage
	err := t.q.each(ctx, "list outbox", query, args, func(rows *sql.Rows) error {
		var (
			m                           state.OutboxMessage
			attempts, next, created, at int64
		)
		if err := rows.Scan(&m.ID, &m.Topic, &m.Key, &m.Payload, &attempts, &next, &m.LastError, &created, &at); err != nil {
			return err
		}
		m.Attempts = int(attempts)
		m.NextAttemptAt, m.CreatedAt, m.SentAt = fromNanos(next), fromNanos(created), fromNanos(at)
		messages = append(messages, m)
		return nil
	})
	return messages, err
}

// MarkOutboxSent records that the effect was performed. Marking it again is
// harmless; an unknown message is NOT_FOUND.
func (t *tx) MarkOutboxSent(ctx context.Context, id int64) error {
	if err := t.writes(tableOutbox, canSelect|canUpdate); err != nil {
		return err
	}
	changed, err := t.q.exec(ctx, "mark message sent",
		`UPDATE outbox SET sent_at = ? WHERE factory_id = ? AND id = ? AND sent_at = 0`,
		toNanos(t.store.now()), string(t.store.factory), id)
	if err != nil || changed == 1 {
		return err
	}
	_, err = t.outboxSent(ctx, id)
	return err
}

// MarkOutboxFailed counts a failed attempt, keeps its reason and holds the
// message back until retryAt. A message that was already sent cannot fail
// any more: CONFLICT.
func (t *tx) MarkOutboxFailed(ctx context.Context, id int64, retryAt time.Time, reason string) error {
	if err := t.writes(tableOutbox, canSelect|canUpdate); err != nil {
		return err
	}
	changed, err := t.q.exec(ctx, "mark message failed", `UPDATE outbox
		SET attempts = attempts + 1, next_attempt_at = ?, last_error = ?
		WHERE factory_id = ? AND id = ? AND sent_at = 0`,
		toNanos(retryAt), cleanText(reason), string(t.store.factory), id)
	if err != nil || changed == 1 {
		return err
	}
	if _, err := t.outboxSent(ctx, id); err != nil {
		return err
	}
	return conflict("outbox message %d was already sent", id)
}

// outboxSent reports whether a message was sent, or NOT_FOUND if it is unknown.
func (t *tx) outboxSent(ctx context.Context, id int64) (bool, error) {
	var sentAt int64
	err := t.q.row(ctx, `SELECT sent_at FROM outbox WHERE factory_id = ? AND id = ?`, string(t.store.factory), id).Scan(&sentAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, notFound("outbox message %d does not exist", id)
	}
	if err != nil {
		return false, t.store.dialect.failure("read outbox message", err)
	}
	return sentAt != 0, nil
}
