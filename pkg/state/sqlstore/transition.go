package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Transition writes one lifecycle step as a compare-and-swap.
//
// The checks run in this order, the same as the state machine's, and the
// first failure wins with nothing written: INVALID_TRANSITION when the
// lifecycle table has no such edge; INVALID_ARGUMENT for a malformed
// request; a replay of an idempotency key
// already used on the issue returns the record written the first time
// (CONFLICT if the key was used for a different step); STALE_FENCING_TOKEN
// when the request carries a token that is not the latest issued for the
// issue's lease; CONFLICT when the stored version or state differs from the
// ones the writer read.
//
// The table check is made here, on every write, so no caller can store a
// step the lifecycle does not contain, such as Intake straight to Closed.
// Guards need proofs the store does not have and stay with the caller, who
// runs statemachine.Machine.Check first.
//
// A write without a fencing token leaves the lease owner and token on the
// record as they were, and a write without a run leaves the run as it was.
// While the run stays the same, the attempt number on the record never goes
// down: a step that would lower it is refused with INVALID_ARGUMENT.
func (t *tx) Transition(ctx context.Context, tr core.Transition) (core.StateRecord, error) {
	none := core.StateRecord{}
	if err := t.writes(tableRecords, canSelect|canInsert|canUpdate); err != nil {
		return none, err
	}
	if err := t.writes(tableTransitions, canSelect|canInsert); err != nil {
		return none, err
	}
	if err := statemachine.CheckEdge(tr); err != nil {
		return none, err
	}
	if err := tr.Validate(); err != nil {
		return none, err
	}
	if first, found, err := t.replayed(ctx, tr); err != nil || found {
		return first, err
	}
	if tr.FencingToken != 0 {
		if err := t.CheckFencing(ctx, state.IssueLeaseResource(tr.IssueID), tr.FencingToken); err != nil {
			return none, err
		}
	}

	record, applied, err := t.compareAndSwap(ctx, tr)
	if err != nil {
		return none, err
	}
	if !applied {
		// On PostgreSQL the same write may have been committed by a
		// concurrent transaction between the replay check and the swap.
		if first, found, err := t.replayed(ctx, tr); err != nil || found {
			return first, err
		}
		return none, t.staleWrite(ctx, tr)
	}
	return record, t.journal(ctx, tr, record)
}

// compareAndSwap writes the record only if the stored version and state are
// the ones the writer read, and reports whether it did.
func (t *tx) compareAndSwap(ctx context.Context, tr core.Transition) (core.StateRecord, bool, error) {
	factory, issue := string(t.store.factory), tr.IssueID.String()
	record := core.StateRecord{
		FactoryID: t.store.factory, IssueID: tr.IssueID, State: tr.To, Version: 1,
		IdempotencyKey: tr.IdempotencyKey, LeaseOwner: tr.LeaseOwner, FencingToken: tr.FencingToken,
		Attempt: tr.Attempt, RunID: tr.RunID, UpdatedAt: t.store.now(),
	}
	if tr.Creates() {
		changed, err := t.q.exec(ctx, "create record", `INSERT INTO records
			(factory_id, issue, state, version, idempotency_key, lease_owner, fencing_token, attempt, run_id, updated_at)
			VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (factory_id, issue) DO NOTHING`,
			factory, issue, string(record.State), record.IdempotencyKey, record.LeaseOwner,
			int64(record.FencingToken), int64(record.Attempt), string(record.RunID), toNanos(record.UpdatedAt))
		return record, changed == 1, err
	}

	current, found, err := t.findRecord(ctx, tr.IssueID)
	if err != nil || !found || current.Version != tr.ExpectedVersion || current.State != tr.From {
		return core.StateRecord{}, false, err
	}
	record.Version = current.Version + 1
	if tr.FencingToken == 0 {
		record.LeaseOwner, record.FencingToken = current.LeaseOwner, current.FencingToken
	}
	if tr.RunID == "" {
		record.RunID = current.RunID
	}
	if record.RunID == current.RunID && record.Attempt < current.Attempt {
		refusal := invalid("attempt %d would lower the attempt %d recorded for the run of issue %s",
			record.Attempt, current.Attempt, tr.IssueID.String())
		refusal.Details = map[string]string{"current_attempt": strconv.Itoa(current.Attempt)}
		return core.StateRecord{}, false, refusal
	}
	// The WHERE clause repeats the comparison, so a writer that committed
	// after the read above still makes this statement change nothing.
	changed, err := t.q.exec(ctx, "write record", `UPDATE records
		SET state = ?, version = ?, idempotency_key = ?, lease_owner = ?, fencing_token = ?, attempt = ?, run_id = ?, updated_at = ?
		WHERE factory_id = ? AND issue = ? AND version = ? AND state = ?`,
		string(record.State), record.Version, record.IdempotencyKey, record.LeaseOwner,
		int64(record.FencingToken), int64(record.Attempt), string(record.RunID), toNanos(record.UpdatedAt),
		factory, issue, tr.ExpectedVersion, string(tr.From))
	return record, changed == 1, err
}

// staleWrite builds the CONFLICT for a write that lost. Its details carry
// what is stored now, so the caller can read again and decide.
func (t *tx) staleWrite(ctx context.Context, tr core.Transition) error {
	current, found, err := t.findRecord(ctx, tr.IssueID)
	if err != nil {
		return err
	}
	refusal := conflict("issue %s is not at version %d in %q; read it again", tr.IssueID.String(), tr.ExpectedVersion, string(tr.From))
	refusal.Details = map[string]string{"current_version": "0", "current_state": ""}
	if found {
		refusal.Details["current_version"] = strconv.FormatInt(current.Version, 10)
		refusal.Details["current_state"] = string(current.State)
	}
	return refusal
}

// replayed looks for an earlier write of the issue under the same
// idempotency key and returns the record that write produced.
func (t *tx) replayed(ctx context.Context, tr core.Transition) (core.StateRecord, bool, error) {
	var (
		toState, event, leaseOwner, runID, candidate, release string
		version, token, attempt, createdAt                    int64
	)
	err := t.q.row(ctx, `SELECT to_state, event, version, lease_owner, fencing_token, attempt, run_id,
		candidate_digest, release_digest, created_at
		FROM transitions WHERE factory_id = ? AND issue = ? AND idempotency_key = ?`,
		string(t.store.factory), tr.IssueID.String(), tr.IdempotencyKey).
		Scan(&toState, &event, &version, &leaseOwner, &token, &attempt, &runID, &candidate, &release, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.StateRecord{}, false, nil
	}
	if err != nil {
		return core.StateRecord{}, false, t.store.dialect.failure("read transition", err)
	}
	// The same key for another step, or for another candidate or release,
	// is not a replay and must not be answered as if it had been written.
	if event != string(tr.Event) || toState != string(tr.To) ||
		candidate != string(tr.CandidateDigest) || release != string(tr.ReleaseDigest) {
		return core.StateRecord{}, false, conflict(
			"idempotency key %q was already used on issue %s for another step", tr.IdempotencyKey, tr.IssueID.String())
	}
	return core.StateRecord{
		FactoryID: t.store.factory, IssueID: tr.IssueID, State: core.State(toState), Version: version,
		IdempotencyKey: tr.IdempotencyKey, LeaseOwner: leaseOwner, FencingToken: core.FencingToken(token),
		Attempt: int(attempt), RunID: core.RunID(runID), UpdatedAt: fromNanos(createdAt),
	}, true, nil
}

// journal keeps the step that produced a record: who asked for it, why, on
// what evidence, for which candidate or release, and the record as it was
// written. The row is what a replay of the same idempotency key returns.
func (t *tx) journal(ctx context.Context, tr core.Transition, record core.StateRecord) error {
	evidence, err := json.Marshal(tr.EvidenceRefs)
	if err != nil {
		return invalid("evidence references cannot be stored")
	}
	_, err = t.q.exec(ctx, "journal transition", `INSERT INTO transitions
		(factory_id, issue, idempotency_key, from_state, to_state, event, version, lease_owner, fencing_token,
		 attempt, run_id, actor_id, reason, evidence_refs, candidate_digest, release_digest, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(record.FactoryID), record.IssueID.String(), record.IdempotencyKey, string(tr.From), string(record.State),
		string(tr.Event), record.Version, record.LeaseOwner, int64(record.FencingToken), int64(record.Attempt),
		string(record.RunID), cleanText(tr.ActorID), cleanText(tr.Reason), string(evidence),
		string(tr.CandidateDigest), string(tr.ReleaseDigest), toNanos(record.UpdatedAt))
	return err
}
