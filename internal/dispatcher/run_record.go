package dispatcher

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// recordNamespace is where the dispatcher keeps its run records in the store.
const recordNamespace = "dispatcher"

// RunStatus is where a run stands as far as the dispatcher is concerned.
type RunStatus string

const (
	// RunWorking means an agent attempt is in flight or about to start.
	RunWorking RunStatus = "working"
	// RunSucceeded means an attempt produced its artifacts; the run waits
	// for evaluation and no agent works on it.
	RunSucceeded RunStatus = "succeeded"
	// RunExhausted means the run used every attempt it was allowed; its
	// reservation was closed (see CloseRunReservation) and nothing more is
	// started for it.
	RunExhausted RunStatus = "exhausted"
)

// AttemptFailure is why one agent attempt did not succeed.
type AttemptFailure struct {
	Attempt int            `json:"attempt"`
	Code    core.ErrorCode `json:"code"`
	Detail  string         `json:"detail,omitempty"`
	At      time.Time      `json:"at"`
}

// RunRecord is what the dispatcher stores about one run, next to the
// lifecycle record of its issue. It is written only under the run's issue
// lease, and it is everything a restarted dispatcher needs to take the run
// over: the brief it works from, the agent run in flight, the attempts made,
// and the artifacts and pull requests reported so far.
type RunRecord struct {
	Issue         core.IssueRef `json:"issue"`
	RunID         core.RunID    `json:"run_id"`
	ReservationID string        `json:"reservation_id"`
	Brief         Brief         `json:"brief"`
	Status        RunStatus     `json:"status"`
	// Attempt counts the agent attempts started, from 1.
	Attempt int `json:"attempt"`
	// Agent is the backend run of the current attempt; nil before it starts.
	Agent            *agent.RunRef    `json:"agent,omitempty"`
	AttemptStartedAt time.Time        `json:"attempt_started_at,omitzero"`
	Failures         []AttemptFailure `json:"failures,omitempty"`
	Artifacts        []agent.Artifact `json:"artifacts,omitempty"`
	PullRequests     []string         `json:"pull_requests,omitempty"`
	// Spend tallies what the attempts' agents reported; see RunSpend.
	Spend     RunSpend  `json:"spend,omitzero"`
	ClaimedAt time.Time `json:"claimed_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func recordKey(run core.RunID) string { return "run/" + string(run) }

// readRunRecord returns the stored record of a run and its version.
func readRunRecord(ctx context.Context, kv state.KV, run core.RunID) (RunRecord, int64, error) {
	entry, err := kv.GetKV(ctx, recordNamespace, recordKey(run))
	if err != nil {
		return RunRecord{}, 0, err
	}
	var record RunRecord
	if err := json.Unmarshal(entry.Value, &record); err != nil || record.RunID != run {
		return RunRecord{}, 0, core.NewError(core.CodeTampered, "the stored record of run "+string(run)+" cannot be read")
	}
	return record, entry.Version, nil
}

// writeRunRecord writes the record at the version it was read at, 0
// creating it, and returns the version written.
func writeRunRecord(ctx context.Context, kv state.KV, record RunRecord, version int64) (int64, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return 0, core.NewError(core.CodeInternal, "a run record cannot be encoded").WithCause(err)
	}
	written, err := kv.PutKV(ctx, state.KVEntry{Namespace: recordNamespace, Key: recordKey(record.RunID), Value: raw, Version: version})
	return written.Version, err
}

// RunRecord returns the stored record of a run, or NOT_FOUND.
func (d *Dispatcher) RunRecord(ctx context.Context, run core.RunID) (RunRecord, error) {
	var record RunRecord
	err := d.cfg.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		record, _, err = readRunRecord(ctx, tx, run)
		return err
	})
	return record, err
}

// StoredRunRecords reads run records from a store opened by another zone,
// such as the control zone, which may read them but never writes them.
type StoredRunRecords struct{ State state.Store }

// RunRecord returns the stored record of a run, or NOT_FOUND.
func (s StoredRunRecords) RunRecord(ctx context.Context, run core.RunID) (RunRecord, error) {
	var record RunRecord
	err := s.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		record, _, err = readRunRecord(ctx, tx, run)
		return err
	})
	return record, err
}
