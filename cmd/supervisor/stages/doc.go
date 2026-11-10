// Package stages is the trusted supervisor's part of the issue lifecycle
// after a candidate was submitted: the independent evaluation, which signs
// its receipt with the supervisor's delegated qa_receipt key, and the
// release of a passed candidate through the release pipeline.
//
// The agent zone only submits. What QA concluded is recorded here, under
// the issue lease, by the control zone: qa_passed with the receipt as its
// proof, or qa_failed under the agent zone's qa/ key so the run's next
// coding attempt can start, within its attempt limit.
package stages

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Namespace is where the supervisor keeps what it decided. Only the
// control zone reads or writes it.
const Namespace = "supervisor"

// Verdicts of an evaluation.
const (
	VerdictPassed = "passed"
	VerdictFailed = "failed"
	// VerdictHeld: no step was written; a person must act, for example
	// because the run used every attempt or the Owner must approve.
	VerdictHeld = "held"
)

// QAResult is the outcome of one evaluator attempt.
type QAResult struct {
	Candidate qa.Candidate   `json:"candidate"`
	Receipt   core.QAReceipt `json:"receipt,omitzero"`
	Verdict   string         `json:"verdict"`
	Reason    string         `json:"reason,omitempty"`
	At        time.Time      `json:"at"`
}

// ReleaseRecord is the outcome of the release of one candidate.
type ReleaseRecord struct {
	Result  string             `json:"result"`
	State   core.State         `json:"state"`
	Release core.ReleaseDigest `json:"release"`
	Reason  string             `json:"reason,omitempty"`
	At      time.Time          `json:"at"`
}

// QAResultKey and ReleaseKey name the records of one run's attempt.
func QAResultKey(run core.RunID, attempt int) string {
	return "qa/" + string(run) + "/" + itoa(attempt)
}
func ReleaseKey(run core.RunID, attempt int) string {
	return "release/" + string(run) + "/" + itoa(attempt)
}

func itoa(n int) string {
	b, _ := json.Marshal(n) // an int always encodes
	return string(b)
}

// get reads a record of the namespace into out.
func get(ctx context.Context, kv state.KV, key string, out any) error {
	entry, err := kv.GetKV(ctx, Namespace, key)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(entry.Value, out); err != nil {
		return core.NewError(core.CodeTampered, "the stored record "+key+" cannot be read")
	}
	return nil
}

// put writes a record of the namespace, replacing what was there.
func put(ctx context.Context, kv state.KV, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return core.NewError(core.CodeInternal, "a record cannot be encoded").WithCause(err)
	}
	version := int64(0)
	if entry, err := kv.GetKV(ctx, Namespace, key); err == nil {
		version = entry.Version
	} else if !core.HasCode(err, core.CodeNotFound) {
		return err
	}
	_, err = kv.PutKV(ctx, state.KVEntry{Namespace: Namespace, Key: key, Value: raw, Version: version})
	return err
}

// ReadQAResult reads the outcome of one evaluator attempt.
func ReadQAResult(ctx context.Context, kv state.KV, run core.RunID, attempt int) (QAResult, error) {
	var r QAResult
	return r, get(ctx, kv, QAResultKey(run, attempt), &r)
}

// ReadRelease reads the outcome of the release of one attempt.
func ReadRelease(ctx context.Context, kv state.KV, run core.RunID, attempt int) (ReleaseRecord, error) {
	var r ReleaseRecord
	return r, get(ctx, kv, ReleaseKey(run, attempt), &r)
}
