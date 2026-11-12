package sqlstore

import (
	"context"
	"database/sql"
	"errors"
)

// AuditRow is one stored link of the audit chain. The store keeps the rows;
// what they mean, and how the chain is hashed and verified, belongs to the
// audit log built on top.
type AuditRow struct {
	Seq      int64
	PrevHash string
	Hash     string
	// Body is the exact bytes Hash was computed over.
	Body []byte
}

// AppendAuditRow adds one row at the end of this factory's chain. seal
// receives the sequence number and the previous hash the new row must carry
// and returns the row's hash and body. Appenders are serialized on the head
// of the chain, so every row follows the one before it without a gap, and
// the row and the new head commit together or not at all.
//
// This is the only way the store writes the audit table; there is none that
// changes or removes a row. It is a transaction of its own and cannot be
// called from inside Update.
func (s *Store) AppendAuditRow(ctx context.Context, seal func(seq int64, prevHash string) (hash string, body []byte, err error)) (AuditRow, error) {
	if err := s.allowed(tableAuditLog, canInsert); err != nil {
		return AuditRow{}, err
	}
	if err := s.allowed(tableAuditHead, canSelect|canInsert|canUpdate); err != nil {
		return AuditRow{}, err
	}
	var row AuditRow
	err := s.inTx(ctx, false, func(ctx context.Context, q *querier) error {
		factory := string(s.factory)
		if _, err := q.exec(ctx, "open audit chain", `INSERT INTO audit_head (factory_id, seq, hash) VALUES (?, 0, '')
			ON CONFLICT (factory_id) DO NOTHING`, factory); err != nil {
			return err
		}
		// Taking the next number locks the head until commit, which is what
		// makes concurrent appenders wait for each other.
		if err := q.row(ctx, `UPDATE audit_head SET seq = seq + 1 WHERE factory_id = ? RETURNING seq, hash`, factory).
			Scan(&row.Seq, &row.PrevHash); err != nil {
			return s.dialect.failure("advance audit chain", err)
		}
		var err error
		if row.Hash, row.Body, err = seal(row.Seq, row.PrevHash); err != nil {
			return err
		}
		if _, err := q.exec(ctx, "append audit record", `INSERT INTO audit_log (factory_id, seq, prev_hash, hash, body)
			VALUES (?, ?, ?, ?, ?)`, factory, row.Seq, row.PrevHash, row.Hash, bytesOrEmpty(row.Body)); err != nil {
			return err
		}
		_, err = q.exec(ctx, "advance audit chain", `UPDATE audit_head SET hash = ? WHERE factory_id = ?`, row.Hash, factory)
		return err
	})
	if err != nil {
		return AuditRow{}, err
	}
	return row, nil
}

// AuditView reads the audit chain of this factory from one snapshot, so the
// rows and the head it returns belong together.
type AuditView struct {
	store *Store
	q     *querier
}

// ViewAudit runs fn against one snapshot of the audit chain.
func (s *Store) ViewAudit(ctx context.Context, fn func(view *AuditView) error) error {
	if err := s.allowed(tableAuditLog, canSelect); err != nil {
		return err
	}
	if err := s.allowed(tableAuditHead, canSelect); err != nil {
		return err
	}
	if fn == nil {
		return invalid("an audit view needs a function to run")
	}
	return s.inTx(ctx, true, func(_ context.Context, q *querier) error { return fn(&AuditView{store: s, q: q}) })
}

// Head returns the sequence number and hash of the last row appended, or 0
// and an empty hash for a chain nothing was appended to.
func (v *AuditView) Head(ctx context.Context) (seq int64, hash string, err error) {
	err = v.q.row(ctx, `SELECT seq, hash FROM audit_head WHERE factory_id = ?`, string(v.store.factory)).Scan(&seq, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	return seq, hash, v.store.dialect.failure("read audit head", err)
}

// Rows returns up to limit rows with a sequence number above afterSeq, in
// order. A limit of zero or less returns them all.
func (v *AuditView) Rows(ctx context.Context, afterSeq int64, limit int) ([]AuditRow, error) {
	query, args := limited(`SELECT seq, prev_hash, hash, body FROM audit_log
		WHERE factory_id = ? AND seq > ? ORDER BY seq`, []any{string(v.store.factory), afterSeq}, limit)
	var out []AuditRow
	err := v.q.each(ctx, "read audit records", query, args, func(rows *sql.Rows) error {
		var row AuditRow
		if err := rows.Scan(&row.Seq, &row.PrevHash, &row.Hash, &row.Body); err != nil {
			return err
		}
		out = append(out, row)
		return nil
	})
	return out, err
}
