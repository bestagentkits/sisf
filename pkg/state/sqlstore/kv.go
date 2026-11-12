package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"unicode/utf8"

	"github.com/bestagentkits/sisf/pkg/state"
)

// GetKV returns one document, or NOT_FOUND.
func (t *tx) GetKV(ctx context.Context, namespace, key string) (state.KVEntry, error) {
	if err := t.readsDocuments(namespace); err != nil {
		return state.KVEntry{}, err
	}
	e := state.KVEntry{Namespace: namespace, Key: key}
	var updatedAt int64
	err := t.q.row(ctx, `SELECT entry_value, version, updated_at FROM kv
		WHERE factory_id = ? AND namespace = ? AND entry_key = ?`, string(t.store.factory), namespace, key).
		Scan(&e.Value, &e.Version, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return state.KVEntry{}, notFound("document %s/%s does not exist", namespace, key)
	}
	if err != nil {
		return state.KVEntry{}, t.store.dialect.failure("read document", err)
	}
	e.UpdatedAt = fromNanos(updatedAt)
	return e, nil
}

// PutKV writes a document only against the version the caller read: version
// 0 creates it and fails if it exists, any other version must be the stored
// one. A lost race is CONFLICT and writes nothing.
func (t *tx) PutKV(ctx context.Context, e state.KVEntry) (state.KVEntry, error) {
	if err := t.writesDocuments(e.Namespace, canInsert|canUpdate); err != nil {
		return state.KVEntry{}, err
	}
	if err := requireText("document namespace", e.Namespace); err != nil {
		return state.KVEntry{}, err
	}
	if err := requireText("document key", e.Key); err != nil {
		return state.KVEntry{}, err
	}
	if e.Version < 0 {
		return state.KVEntry{}, invalid("document version %d must not be negative", e.Version)
	}
	read := e.Version
	e.Version, e.UpdatedAt = read+1, t.store.now()
	factory := string(t.store.factory)
	var (
		changed int64
		err     error
	)
	if read == 0 {
		changed, err = t.q.exec(ctx, "create document", `INSERT INTO kv
			(factory_id, namespace, entry_key, entry_value, version, updated_at) VALUES (?, ?, ?, ?, 1, ?)
			ON CONFLICT (factory_id, namespace, entry_key) DO NOTHING`,
			factory, e.Namespace, e.Key, bytesOrEmpty(e.Value), toNanos(e.UpdatedAt))
	} else {
		changed, err = t.q.exec(ctx, "write document", `UPDATE kv SET entry_value = ?, version = ?, updated_at = ?
			WHERE factory_id = ? AND namespace = ? AND entry_key = ? AND version = ?`,
			bytesOrEmpty(e.Value), e.Version, toNanos(e.UpdatedAt), factory, e.Namespace, e.Key, read)
	}
	if err != nil {
		return state.KVEntry{}, err
	}
	if changed != 1 {
		return state.KVEntry{}, conflict("document %s/%s is not at version %d; read it again", e.Namespace, e.Key, read)
	}
	return e, nil
}

// ListKV returns the documents of a namespace whose key starts with prefix,
// in byte order of the key. The prefix is matched literally and with case.
func (t *tx) ListKV(ctx context.Context, namespace, prefix string, limit int) ([]state.KVEntry, error) {
	if err := t.readsDocuments(namespace); err != nil {
		return nil, err
	}
	// substr counts characters on both engines, unlike LIKE, whose wildcards
	// and case folding differ between them. The cast states the type of the
	// length for PostgreSQL, whatever way the argument is sent.
	query, args := limited(`SELECT entry_key, entry_value, version, updated_at FROM kv
		WHERE factory_id = ? AND namespace = ? AND substr(entry_key, 1, CAST(? AS INTEGER)) = ? ORDER BY entry_key`,
		[]any{string(t.store.factory), namespace, int64(utf8.RuneCountInString(prefix)), prefix}, limit)
	var entries []state.KVEntry
	err := t.q.each(ctx, "list documents", query, args, func(rows *sql.Rows) error {
		e := state.KVEntry{Namespace: namespace}
		var updatedAt int64
		if err := rows.Scan(&e.Key, &e.Value, &e.Version, &updatedAt); err != nil {
			return err
		}
		e.UpdatedAt = fromNanos(updatedAt)
		entries = append(entries, e)
		return nil
	})
	return entries, err
}

// DeleteKV removes a document only at the version the caller read: CONFLICT
// if it moved on, NOT_FOUND if it does not exist.
func (t *tx) DeleteKV(ctx context.Context, namespace, key string, version int64) error {
	if err := t.writesDocuments(namespace, canSelect|canDelete); err != nil {
		return err
	}
	changed, err := t.q.exec(ctx, "delete document",
		`DELETE FROM kv WHERE factory_id = ? AND namespace = ? AND entry_key = ? AND version = ?`,
		string(t.store.factory), namespace, key, version)
	if err != nil || changed == 1 {
		return err
	}
	if _, err := t.GetKV(ctx, namespace, key); err != nil {
		return err
	}
	return conflict("document %s/%s is not at version %d; read it again", namespace, key, version)
}
