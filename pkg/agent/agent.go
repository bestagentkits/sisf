// Package agent defines the contract between the factory and a coding or QA
// agent runtime, built in or remote.
//
// A backend only executes. Orchestration, run state and every decision stay
// inside the factory deployment, whichever backend does the work. Abilities
// beyond the required interface are optional interfaces; a backend that lacks
// one says so in its capabilities and answers NOT_SUPPORTED, and never
// pretends.
package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Role is the part an agent plays in a run. The coder and the evaluator of
// one candidate are always different identities.
type Role string

const (
	RoleCoder Role = "coder"
	RoleQA    Role = "qa"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return r == RoleCoder || r == RoleQA }

// Handoff is the structured context a run starts from. A fresh agent with no
// chat history must be able to do the work from this and the repository.
type Handoff struct {
	SoW       string   `json:"sow"`
	DoD       string   `json:"dod"`
	Decisions []string `json:"decisions,omitempty"`
	Evidence  []string `json:"evidence,omitempty"`
}

// Limits bound a run. They apply whatever the budget mode is.
type Limits struct {
	Timeout  time.Duration `json:"timeout"`
	MaxTurns int           `json:"max_turns,omitempty"`
}

// Task is one unit of work handed to a backend.
type Task struct {
	RunID   core.RunID    `json:"run_id"`
	IssueID core.IssueRef `json:"issue_id"`
	Role    Role          `json:"role"`
	Step    core.StepKind `json:"step"`
	Handoff Handoff       `json:"handoff"`
	// Policy pins the policy and the model route the run executes under.
	Policy core.PolicyRefs `json:"policy"`
	Limits Limits          `json:"limits"`
	// IdempotencyKey lets a backend start the task once when Start is retried.
	IdempotencyKey string `json:"idempotency_key"`
}

// Validate rejects a task that lacks scope, completion criteria, pinned
// policy or a time limit.
func (t Task) Validate() error {
	if err := t.RunID.Validate(); err != nil {
		return err
	}
	if err := t.IssueID.Validate(); err != nil {
		return err
	}
	if err := t.Policy.Validate(); err != nil {
		return err
	}
	switch {
	case !t.Role.Valid():
		return core.NewError(core.CodeInvalidArgument, "task has an unknown agent role")
	case !t.Step.Valid():
		return core.NewError(core.CodeInvalidArgument, "task has an unknown step kind")
	case strings.TrimSpace(t.Handoff.SoW) == "" || strings.TrimSpace(t.Handoff.DoD) == "":
		return core.NewError(core.CodeInvalidArgument, "task needs a scope of work and a definition of done")
	case t.Limits.Timeout <= 0 || t.Limits.MaxTurns < 0:
		return core.NewError(core.CodeInvalidArgument, "task needs a positive timeout and a non-negative turn limit")
	case t.IdempotencyKey == "":
		return core.NewError(core.CodeInvalidArgument, "task needs an idempotency key")
	}
	return nil
}

// WorkspaceRef names the task workspace an agent works in. The workspace is
// created by an execution backend and carries its file, network and time limits.
type WorkspaceRef struct {
	ID      string         `json:"id"`
	Path    string         `json:"path,omitempty"`
	BaseSHA core.CommitSHA `json:"base_sha,omitempty"`
}

// RunRef is a backend's handle for a started run. ID belongs to the backend;
// RunID is the factory's own identifier and the only one stored in state.
type RunRef struct {
	Backend string     `json:"backend"`
	ID      string     `json:"id"`
	RunID   core.RunID `json:"run_id"`
}

// Phase is where a run stands.
type Phase string

const (
	PhaseQueued       Phase = "queued"
	PhaseRunning      Phase = "running"
	PhaseWaitingInput Phase = "waiting_input"
	// PhasePaused means the run cannot progress, for example during a
	// provider outage. It is not finished and is never treated as success.
	PhasePaused    Phase = "paused"
	PhaseSucceeded Phase = "succeeded"
	PhaseFailed    Phase = "failed"
	PhaseCancelled Phase = "cancelled"
)

var allPhases = []Phase{PhaseQueued, PhaseRunning, PhaseWaitingInput, PhasePaused, PhaseSucceeded, PhaseFailed, PhaseCancelled}

// Phases returns every phase. The caller owns the returned slice.
func Phases() []Phase { return slices.Clone(allPhases) }

// Valid reports whether p is a known phase.
func (p Phase) Valid() bool { return slices.Contains(allPhases, p) }

// Terminal reports whether the run has finished, successfully or not.
func (p Phase) Terminal() bool {
	return p == PhaseSucceeded || p == PhaseFailed || p == PhaseCancelled
}

// Status is the current state of a run as the backend reports it.
type Status struct {
	Phase     Phase     `json:"phase"`
	Detail    string    `json:"detail,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Cursor marks a position in a run's event stream. The empty cursor is the start.
type Cursor string

// EventKind is the type of a run event. Consumers switch on it and ignore
// kinds they do not know.
type EventKind string

const (
	EventMessage    EventKind = "message"
	EventToolCall   EventKind = "tool_call"
	EventToolResult EventKind = "tool_result"
	EventStatus     EventKind = "status"
	EventUsage      EventKind = "usage"
	EventCheckpoint EventKind = "checkpoint"
	EventError      EventKind = "error"
)

// Valid reports whether k is a known event kind.
func (k EventKind) Valid() bool {
	return slices.Contains([]EventKind{EventMessage, EventToolCall, EventToolResult, EventStatus, EventUsage, EventCheckpoint, EventError}, k)
}

// Event is one entry of a run's append-only event stream. Payloads are
// redacted before they are stored or shown.
type Event struct {
	Cursor  Cursor          `json:"cursor"`
	At      time.Time       `json:"at"`
	Kind    EventKind       `json:"kind"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// EventPage is a batch of events and the cursor to continue from.
type EventPage struct {
	Events []Event `json:"events"`
	Next   Cursor  `json:"next"`
}

// ArtifactKind is the type of output a run leaves behind.
type ArtifactKind string

const (
	ArtifactPatch      ArtifactKind = "patch"
	ArtifactTestReport ArtifactKind = "test_report"
	ArtifactBuild      ArtifactKind = "build"
	ArtifactLog        ArtifactKind = "log"
	ArtifactTranscript ArtifactKind = "transcript"
)

// Valid reports whether k is a known artifact kind.
func (k ArtifactKind) Valid() bool {
	return slices.Contains([]ArtifactKind{ArtifactPatch, ArtifactTestReport, ArtifactBuild, ArtifactLog, ArtifactTranscript}, k)
}

// Artifact is one output of a run, identified by content digest.
type Artifact struct {
	Name   string       `json:"name"`
	Kind   ArtifactKind `json:"kind"`
	Digest core.Digest  `json:"digest"`
	URI    string       `json:"uri"`
	Size   int64        `json:"size"`
}

// Backend runs agents. Capabilities are read before a backend is used.
// Provider credentials never appear in events, artifacts or errors.
type Backend interface {
	Name() string
	Capabilities(ctx context.Context) (core.CapabilitySet, error)
	// Start begins the task in the workspace with exactly the given scopes.
	Start(ctx context.Context, task Task, workspace WorkspaceRef, scopes []string) (RunRef, error)
	Status(ctx context.Context, run RunRef) (Status, error)
	// Events returns the events after cursor.
	Events(ctx context.Context, run RunRef, cursor Cursor) (EventPage, error)
	Cancel(ctx context.Context, run RunRef) error
	CollectArtifacts(ctx context.Context, run RunRef) ([]Artifact, error)
}

// Resumer continues an interrupted run from a structured handoff.
type Resumer interface {
	Resume(ctx context.Context, run RunRef, handoff Handoff) (RunRef, error)
}

// Checkpointer saves a point a run can later be resumed from.
type Checkpointer interface {
	Checkpoint(ctx context.Context, run RunRef) (checkpointID string, err error)
}

// UsageReporter reports what a run consumed. Amounts the provider cannot
// confirm are returned with Verified false.
type UsageReporter interface {
	Usage(ctx context.Context, run RunRef) ([]core.Usage, error)
}

// HumanInputReceiver passes a person's answer to a run waiting for input.
type HumanInputReceiver interface {
	HumanInput(ctx context.Context, run RunRef, input string) error
}

// SessionLinker returns a link to the run's live session.
type SessionLinker interface {
	SessionLink(ctx context.Context, run RunRef) (string, error)
}

// WebhookHandler authenticates and normalizes a callback from a remote agent
// service. An unauthenticated callback is rejected, never applied.
type WebhookHandler interface {
	HandleWebhook(ctx context.Context, header http.Header, body []byte) (RunRef, []Event, error)
}
