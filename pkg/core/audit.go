package core

import (
	"context"
	"time"
)

// AuditOutcome is what happened to an audited action.
type AuditOutcome string

const (
	// OutcomeAllowed records that authorization passed.
	OutcomeAllowed AuditOutcome = "allowed"
	// OutcomeDenied records that authorization or an approval check refused.
	OutcomeDenied AuditOutcome = "denied"
	// OutcomeExecuted records that the action took effect.
	OutcomeExecuted AuditOutcome = "executed"
	// OutcomeFailed records that the action was attempted and did not take effect.
	OutcomeFailed AuditOutcome = "failed"
)

// Valid reports whether o is a known outcome.
func (o AuditOutcome) Valid() bool {
	return o == OutcomeAllowed || o == OutcomeDenied || o == OutcomeExecuted || o == OutcomeFailed
}

// AuditEvent is one record of who did what, under which policy, with what
// result. Seq, PrevHash and Hash are assigned by the log and chain each
// record to the one before it.
type AuditEvent struct {
	Seq       int64     `json:"seq"`
	At        time.Time `json:"at"`
	FactoryID FactoryID `json:"factory_id"`
	ActorID   string    `json:"actor_id"`
	ActorKind ActorKind `json:"actor_kind"`
	Surface   Surface   `json:"surface,omitempty"`
	// Action is the action name, or the lifecycle event for a state change.
	Action   string `json:"action"`
	Resource string `json:"resource,omitempty"`
	// SubjectDigest binds the record to the exact content that was acted on.
	SubjectDigest Digest            `json:"subject_digest,omitempty"`
	PolicySHA     CommitSHA         `json:"policy_sha,omitempty"`
	IssueID       IssueRef          `json:"issue_id,omitzero"`
	RunID         RunID             `json:"run_id,omitempty"`
	Outcome       AuditOutcome      `json:"outcome"`
	Details       map[string]string `json:"details,omitempty"`
	PrevHash      string            `json:"prev_hash"`
	Hash          string            `json:"hash"`
}

// AuditLog is the append-only, hash-chained audit trail. It offers no way to
// change or remove a record, and no role, the Owner included, gets one.
type AuditLog interface {
	// Append writes the event at the end of the chain and returns it with
	// Seq, PrevHash and Hash filled in. Values the caller set for those
	// fields are ignored.
	Append(ctx context.Context, e AuditEvent) (AuditEvent, error)
	// Verify walks the chain and returns TAMPERED when a record was edited,
	// removed or reordered.
	Verify(ctx context.Context) error
}

// AuditReader is the optional read side of an audit log.
type AuditReader interface {
	// Events returns up to limit records with Seq greater than afterSeq, in order.
	Events(ctx context.Context, afterSeq int64, limit int) ([]AuditEvent, error)
}
