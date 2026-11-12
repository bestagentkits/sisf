package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

const recordColumns = `issue, state, version, idempotency_key, lease_owner, fencing_token, attempt, run_id,
	candidate_digest, release_digest, updated_at`

// scanRecord reads one row selected with recordColumns.
func (t *tx) scanRecord(scan func(dest ...any) error) (core.StateRecord, error) {
	var (
		issue, stateText, key, leaseOwner, runID, candidate, release string
		version, token, attempt, updatedAt                           int64
	)
	if err := scan(&issue, &stateText, &version, &key, &leaseOwner, &token, &attempt, &runID,
		&candidate, &release, &updatedAt); err != nil {
		return core.StateRecord{}, err
	}
	ref, err := core.ParseIssueRef(issue)
	if err != nil {
		return core.StateRecord{}, err
	}
	return core.StateRecord{
		FactoryID: t.store.factory, IssueID: ref, State: core.State(stateText), Version: version,
		IdempotencyKey: key, LeaseOwner: leaseOwner, FencingToken: core.FencingToken(token),
		Attempt: int(attempt), RunID: core.RunID(runID), CandidateDigest: core.Digest(candidate),
		ReleaseDigest: core.ReleaseDigest(release), UpdatedAt: fromNanos(updatedAt),
	}, nil
}

// GetRecord returns the lifecycle record of an issue, or NOT_FOUND.
func (t *tx) GetRecord(ctx context.Context, issue core.IssueRef) (core.StateRecord, error) {
	if err := t.reads(tableRecords); err != nil {
		return core.StateRecord{}, err
	}
	record, found, err := t.findRecord(ctx, issue)
	if err != nil {
		return core.StateRecord{}, err
	}
	if !found {
		return core.StateRecord{}, notFound("issue %s has no record", issue.String())
	}
	return record, nil
}

// findRecord reads the record of an issue and reports whether it exists.
func (t *tx) findRecord(ctx context.Context, issue core.IssueRef) (core.StateRecord, bool, error) {
	row := t.q.row(ctx, `SELECT `+recordColumns+` FROM records WHERE factory_id = ? AND issue = ?`,
		string(t.store.factory), issue.String())
	record, err := t.scanRecord(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return core.StateRecord{}, false, nil
	}
	if err != nil {
		return core.StateRecord{}, false, t.store.dialect.failure("read record", err)
	}
	return record, true, nil
}

// ListRecords returns the records the filter selects, least recently written
// first, so a caller that reconciles can continue from the last one it saw.
func (t *tx) ListRecords(ctx context.Context, filter state.RecordFilter) ([]core.StateRecord, error) {
	if err := t.reads(tableRecords); err != nil {
		return nil, err
	}
	query := `SELECT ` + recordColumns + ` FROM records WHERE factory_id = ?`
	args := []any{string(t.store.factory)}
	if len(filter.States) > 0 {
		marks := make([]string, len(filter.States))
		for i, s := range filter.States {
			marks[i] = "?"
			args = append(args, string(s))
		}
		query += ` AND state IN (` + strings.Join(marks, ", ") + `)`
	}
	if !filter.UpdatedSince.IsZero() {
		query += ` AND updated_at >= ?`
		args = append(args, toNanos(filter.UpdatedSince))
	}
	query, args = limited(query+` ORDER BY updated_at, issue`, args, filter.Limit)

	var records []core.StateRecord
	err := t.q.each(ctx, "list records", query, args, func(rows *sql.Rows) error {
		record, err := t.scanRecord(rows.Scan)
		records = append(records, record)
		return err
	})
	return records, err
}
