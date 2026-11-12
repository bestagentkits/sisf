package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Transition writes one lifecycle step as a compare-and-swap. Every check of
// the Records contract is made here, on every write, so a caller that never
// asked the lifecycle machine still cannot store a step it forbids.
//
// The checks run in this order and the first failure wins with nothing
// written:
//
//   - PERMISSION_DENIED when the zone of the store may not write the step:
//     each zone has its own list of lifecycle events (zone_ownership.go);
//   - INVALID_TRANSITION when the lifecycle table has no such edge;
//   - INVALID_ARGUMENT for a malformed request;
//   - a replay of an idempotency key already used on the issue returns the
//     record written the first time (CONFLICT if the key was used for a
//     different step, run, candidate or release);
//   - CONFLICT when the stored version or state differs from the ones the
//     writer read;
//   - STALE_FENCING_TOKEN when a run holds the issue, or the step is the
//     claim, and the request carries no lease, and for any request whose
//     token is not the latest one issued for the issue's lease;
//   - PERMISSION_DENIED when the zone may not write the step under its
//     idempotency key: a zone with keys of its own stays within them
//     (zone_ownership.go), checked here, where the database's row policy
//     refuses it as well;
//   - CONFLICT when the step does not continue the run, the candidate or the
//     release the record names, and when a claim names a run the journal
//     shows the issue has had before;
//   - INVALID_ARGUMENT when the step would lower the attempt count of the run.
//
// Guards need proofs the store does not have and stay with the caller, who
// runs statemachine.Machine.Check first.
//
// The record written carries the lease the step was written under, so a step
// without one leaves none on the record. The run changes only on the claim,
// which also forgets the candidate and the release of the run before it; the
// candidate changes only when one is submitted and the release only when its
// rollout starts.
func (t *tx) Transition(ctx context.Context, tr core.Transition) (core.StateRecord, error) {
	none := core.StateRecord{}
	if err := t.writesStep(tr); err != nil {
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
// the ones the writer read and the step may follow the stored record, and
// reports whether it wrote.
func (t *tx) compareAndSwap(ctx context.Context, tr core.Transition) (core.StateRecord, bool, error) {
	none := core.StateRecord{}
	// A record that does not exist yet is the zero record: no run holds it
	// and it names nothing.
	current := core.StateRecord{FactoryID: t.store.factory, IssueID: tr.IssueID}
	if !tr.Creates() {
		stored, found, err := t.findRecord(ctx, tr.IssueID)
		if err != nil || !found || stored.Version != tr.ExpectedVersion || stored.State != tr.From {
			return none, false, err
		}
		current = stored
	}
	if err := t.checkWriter(ctx, current, tr); err != nil {
		return none, false, err
	}
	record := written(current, tr, t.store.now())

	factory, issue := string(t.store.factory), tr.IssueID.String()
	if tr.Creates() {
		changed, err := t.q.exec(ctx, "create record", `INSERT INTO records
			(factory_id, issue, state, version, idempotency_key, lease_owner, fencing_token, attempt, run_id,
			 candidate_digest, release_digest, updated_at)
			VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (factory_id, issue) DO NOTHING`,
			factory, issue, string(record.State), record.IdempotencyKey, record.LeaseOwner,
			int64(record.FencingToken), int64(record.Attempt), string(record.RunID),
			string(record.CandidateDigest), string(record.ReleaseDigest), toNanos(record.UpdatedAt))
		return record, changed == 1, err
	}
	// The WHERE clause repeats the comparison, so a writer that committed
	// after the read above still makes this statement change nothing.
	changed, err := t.q.exec(ctx, "write record", `UPDATE records
		SET state = ?, version = ?, idempotency_key = ?, lease_owner = ?, fencing_token = ?, attempt = ?, run_id = ?,
		    candidate_digest = ?, release_digest = ?, updated_at = ?
		WHERE factory_id = ? AND issue = ? AND version = ? AND state = ?`,
		string(record.State), record.Version, record.IdempotencyKey, record.LeaseOwner,
		int64(record.FencingToken), int64(record.Attempt), string(record.RunID),
		string(record.CandidateDigest), string(record.ReleaseDigest), toNanos(record.UpdatedAt),
		factory, issue, tr.ExpectedVersion, string(tr.From))
	return record, changed == 1, err
}

// checkWriter reports whether tr is written by the holder of the issue and
// continues what the stored record is about. current is the record as it is
// before the write.
func (t *tx) checkWriter(ctx context.Context, current core.StateRecord, tr core.Transition) error {
	// From the claim until the release leaves Monitoring a step must carry a
	// lease, and whatever token a step carries must be the latest one issued:
	// a worker that lost its lease writes neither with its old token nor
	// with none.
	if err := statemachine.CheckLease(tr); err != nil {
		return err
	}
	if tr.FencingToken != 0 {
		if err := t.CheckFencing(ctx, state.IssueLeaseResource(tr.IssueID), tr.FencingToken); err != nil {
			return err
		}
	}
	if err := t.keysStep(tr); err != nil {
		return err
	}
	if err := statemachine.CheckRecordBinding(current, tr); err != nil {
		return err
	}
	if tr.Event == core.EventClaimed {
		if err := t.checkNewRun(ctx, tr); err != nil {
			return err
		}
	}
	// While the run stays the same its attempt count never goes down; only
	// the claim starts a run, and with it a count of its own.
	if tr.Event != core.EventClaimed && tr.Attempt < current.Attempt {
		refusal := invalid("attempt %d would lower the attempt %d recorded for the run of issue %s",
			tr.Attempt, current.Attempt, tr.IssueID.String())
		refusal.Details = map[string]string{"current_attempt": strconv.Itoa(current.Attempt)}
		return refusal
	}
	return nil
}

// checkNewRun reports whether the claim tr names a run the issue has never
// had. The binding check sees only the run the record names now; the
// journal keeps every run that ever wrote a step of the issue, so a claim
// cannot bring back a run from before the last one either, and with it
// receipts that run collected. Such a claim is CONFLICT.
func (t *tx) checkNewRun(ctx context.Context, tr core.Transition) error {
	var steps int64
	err := t.q.row(ctx, `SELECT COUNT(*) FROM transitions WHERE factory_id = ? AND issue = ? AND run_id = ?`,
		string(t.store.factory), tr.IssueID.String(), string(tr.RunID)).Scan(&steps)
	if err != nil {
		return t.store.dialect.failure("read the runs of the issue", err)
	}
	if steps > 0 {
		refusal := conflict("issue %s already had run %s; a claim starts a run the issue has not had", tr.IssueID.String(), string(tr.RunID))
		refusal.Details = map[string]string{"from": string(tr.From), "event": string(tr.Event), "to": string(tr.To)}
		return refusal
	}
	return nil
}

// written returns the record as it is after tr was applied to current.
func written(current core.StateRecord, tr core.Transition, now time.Time) core.StateRecord {
	record := current
	record.State, record.Version, record.IdempotencyKey = tr.To, current.Version+1, tr.IdempotencyKey
	record.LeaseOwner, record.FencingToken, record.Attempt = tr.LeaseOwner, tr.FencingToken, tr.Attempt
	record.UpdatedAt = now
	switch tr.Event {
	case core.EventClaimed:
		record.RunID, record.CandidateDigest, record.ReleaseDigest = tr.RunID, "", ""
	case core.EventCandidateSubmitted:
		record.CandidateDigest = tr.CandidateDigest
	case core.EventRolloutStarted:
		record.ReleaseDigest = tr.ReleaseDigest
	}
	return record
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
		recordCandidate, recordRelease                        string
		version, token, attempt, createdAt                    int64
	)
	err := t.q.row(ctx, `SELECT to_state, event, version, lease_owner, fencing_token, attempt, run_id,
		candidate_digest, release_digest, record_candidate_digest, record_release_digest, created_at
		FROM transitions WHERE factory_id = ? AND issue = ? AND idempotency_key = ?`,
		string(t.store.factory), tr.IssueID.String(), tr.IdempotencyKey).
		Scan(&toState, &event, &version, &leaseOwner, &token, &attempt, &runID,
			&candidate, &release, &recordCandidate, &recordRelease, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.StateRecord{}, false, nil
	}
	if err != nil {
		return core.StateRecord{}, false, t.store.dialect.failure("read transition", err)
	}
	// The same key for another step, or for another run, candidate or
	// release, is not a replay and must not be answered as if it had been
	// written.
	if event != string(tr.Event) || toState != string(tr.To) || (tr.RunID != "" && runID != string(tr.RunID)) ||
		candidate != string(tr.CandidateDigest) || release != string(tr.ReleaseDigest) {
		return core.StateRecord{}, false, conflict(
			"idempotency key %q was already used on issue %s for another step", tr.IdempotencyKey, tr.IssueID.String())
	}
	return core.StateRecord{
		FactoryID: t.store.factory, IssueID: tr.IssueID, State: core.State(toState), Version: version,
		IdempotencyKey: tr.IdempotencyKey, LeaseOwner: leaseOwner, FencingToken: core.FencingToken(token),
		Attempt: int(attempt), RunID: core.RunID(runID), CandidateDigest: core.Digest(recordCandidate),
		ReleaseDigest: core.ReleaseDigest(recordRelease), UpdatedAt: fromNanos(createdAt),
	}, true, nil
}

// journal keeps the step that produced a record: who asked for it, why, on
// what evidence, which candidate or release it named, and the record as it
// was written. The row is what a replay of the same idempotency key returns.
func (t *tx) journal(ctx context.Context, tr core.Transition, record core.StateRecord) error {
	evidence, err := json.Marshal(tr.EvidenceRefs)
	if err != nil {
		return invalid("evidence references cannot be stored")
	}
	_, err = t.q.exec(ctx, "journal transition", `INSERT INTO transitions
		(factory_id, issue, idempotency_key, from_state, to_state, event, version, lease_owner, fencing_token,
		 attempt, run_id, actor_id, reason, evidence_refs, candidate_digest, release_digest,
		 record_candidate_digest, record_release_digest, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(record.FactoryID), record.IssueID.String(), record.IdempotencyKey, string(tr.From), string(record.State),
		string(tr.Event), record.Version, record.LeaseOwner, int64(record.FencingToken), int64(record.Attempt),
		string(record.RunID), cleanText(tr.ActorID), cleanText(tr.Reason), string(evidence),
		string(tr.CandidateDigest), string(tr.ReleaseDigest),
		string(record.CandidateDigest), string(record.ReleaseDigest), toNanos(record.UpdatedAt))
	return err
}
