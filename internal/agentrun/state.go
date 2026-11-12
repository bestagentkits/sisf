package agentrun

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Namespace and the key prefix are where run states are stored. The namespace
// is the agent zone's own, so the controller needs no extra grant; the prefix
// keeps its documents apart from the dispatcher's.
const (
	Namespace = "dispatcher"
	keyPrefix = "agentrun/run/"
)

// Status is where a run stands between attempts.
type Status string

const (
	// StatusRunning means an attempt is in flight or about to start.
	StatusRunning Status = "running"
	// StatusAwaitingQA means an attempt produced a candidate that waits for
	// the independent evaluation.
	StatusAwaitingQA Status = "awaiting_qa"
	// StatusInterrupted means the attempt ended without a result, for example
	// because its process was lost. Resume starts a fresh agent.
	StatusInterrupted Status = "interrupted"
	// StatusFailed means the agent reported a failure. Retry moves up the
	// ladder.
	StatusFailed Status = "failed"
	// StatusPassed means QA accepted the candidate.
	StatusPassed Status = "passed"
	// StatusExhausted means the ladder has no rung left.
	StatusExhausted Status = "exhausted"
)

// Outcome is how one attempt ended.
type Outcome string

const (
	OutcomeSubmitted   Outcome = "submitted"
	OutcomeQAPassed    Outcome = "qa_passed"
	OutcomeQAFailed    Outcome = "qa_failed"
	OutcomeAgentFailed Outcome = "agent_failed"
	OutcomeInterrupted Outcome = "interrupted"
)

// Attempt is one agent process working on a run. Nothing of its conversation
// is kept: only what the factory observed from outside.
type Attempt struct {
	Number int             `json:"number"`
	Rung   string          `json:"rung"`
	Route  core.ModelRoute `json:"route"`
	// Verify says the agent was told to check its own work.
	Verify bool `json:"verify"`
	// HandoffDigest is the digest of the handoff the attempt started from.
	HandoffDigest core.Digest `json:"handoff_digest"`
	Backend       string      `json:"backend,omitempty"`
	// Agent is the backend run; nil until the backend accepted the start.
	Agent       *agent.RunRef    `json:"agent,omitempty"`
	ToolVersion string           `json:"tool_version,omitempty"`
	StartedAt   time.Time        `json:"started_at"`
	EndedAt     time.Time        `json:"ended_at,omitzero"`
	Outcome     Outcome          `json:"outcome,omitempty"`
	Detail      string           `json:"detail,omitempty"`
	Artifacts   []agent.Artifact `json:"artifacts,omitempty"`
	Usage       []core.Usage     `json:"usage,omitempty"`
}

// RunState is everything needed to continue a run in a new process: what the
// work is, what has been decided and observed, and which attempt is current.
type RunState struct {
	RunID     core.RunID         `json:"run_id"`
	Issue     core.IssueRef      `json:"issue"`
	Step      core.StepKind      `json:"step"`
	Policy    core.PolicyRefs    `json:"policy"`
	Limits    agent.Limits       `json:"limits"`
	Workspace agent.WorkspaceRef `json:"workspace"`
	Scopes    []string           `json:"scopes,omitempty"`
	// Handoff is the summary the current attempt started from and the next
	// one will start from, once updated.
	Handoff  agent.Handoff `json:"handoff"`
	Status   Status        `json:"status"`
	Ladder   Position      `json:"ladder"`
	Attempts []Attempt     `json:"attempts"`
	// Verdicts counts QA failures reported; it is the number of times the run
	// was run again because it failed QA.
	QAFailures int       `json:"qa_failures"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Current returns the latest attempt, or nil.
func (s *RunState) Current() *Attempt {
	if len(s.Attempts) == 0 {
		return nil
	}
	return &s.Attempts[len(s.Attempts)-1]
}

func stateKey(run core.RunID) string { return keyPrefix + string(run) }

// load reads a run state and its version; NOT_FOUND when there is none.
func load(ctx context.Context, store state.Store, run core.RunID) (RunState, int64, error) {
	var (
		st      RunState
		version int64
	)
	err := store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, Namespace, stateKey(run))
		if err != nil {
			return err
		}
		version = entry.Version
		if err := json.Unmarshal(entry.Value, &st); err != nil || st.RunID != run {
			return core.NewError(core.CodeTampered, "the stored state of run "+string(run)+" cannot be read")
		}
		return nil
	})
	return st, version, err
}

// save writes the state against the version it was read at (0 creates) and
// returns the new version. A lost race is CONFLICT.
func save(ctx context.Context, store state.Store, st RunState, version int64, now time.Time) (int64, error) {
	st.UpdatedAt = now
	raw, err := json.Marshal(st)
	if err != nil {
		return 0, core.NewError(core.CodeInternal, "a run state cannot be encoded").WithCause(err)
	}
	var written int64
	err = store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.PutKV(ctx, state.KVEntry{Namespace: Namespace, Key: stateKey(st.RunID), Value: raw, Version: version})
		written = entry.Version
		return err
	})
	return written, err
}
