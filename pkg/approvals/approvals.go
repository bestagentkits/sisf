// Package approvals defines the contract of human approval for sensitive
// actions.
//
// An approval is bound to the digest of the exact thing approved and to the
// policy revision in force when it was given. It carries a nonce, expires,
// can be used once and records who decided and through which surface. A
// change to the subject or to the policy after the decision voids it.
package approvals

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/core"
)

// MinNonceLength is the shortest nonce accepted, in characters.
const MinNonceLength = 16

// Decision is what an approver answered.
type Decision string

const (
	DecisionApprove        Decision = "approve"
	DecisionReject         Decision = "reject"
	DecisionRequestChanges Decision = "request_changes"
)

// Valid reports whether d is a known decision.
func (d Decision) Valid() bool {
	return d == DecisionApprove || d == DecisionReject || d == DecisionRequestChanges
}

// Status is where an approval request stands.
type Status string

const (
	StatusPending          Status = "pending"
	StatusApproved         Status = "approved"
	StatusRejected         Status = "rejected"
	StatusChangesRequested Status = "changes_requested"
	StatusExpired          Status = "expired"
	// StatusConsumed means the approval was used for its one action.
	StatusConsumed Status = "consumed"
)

var allStatuses = []Status{StatusPending, StatusApproved, StatusRejected, StatusChangesRequested, StatusExpired, StatusConsumed}

// Statuses returns the six statuses. The caller owns the returned slice.
func Statuses() []Status { return slices.Clone(allStatuses) }

// Valid reports whether s is a known status.
func (s Status) Valid() bool { return slices.Contains(allStatuses, s) }

// Open reports whether the request can still be decided.
func (s Status) Open() bool { return s == StatusPending }

// Request asks an authorized person to decide on one action. It is what the
// approval inbox shows on every surface.
type Request struct {
	ID        string         `json:"id"`
	FactoryID core.FactoryID `json:"factory_id"`
	// IssueID is zero when the request concerns no single issue.
	IssueID core.IssueRef `json:"issue_id,omitzero"`
	// Action is the name of the action that will run once approved.
	Action string `json:"action"`
	// SubjectDigest identifies exactly what is being approved: a scope, a
	// release, a policy change. Approval of one digest never covers another.
	SubjectDigest core.Digest `json:"subject_digest"`
	// PolicyRevision is the policy commit the request was evaluated under.
	PolicyRevision core.CommitSHA `json:"policy_revision"`
	// Nonce is random, unique to the request and echoed by the decision.
	Nonce       string    `json:"nonce"`
	ExpiresAt   time.Time `json:"expires_at"`
	RequestedBy string    `json:"requested_by"`
	// RiskSummary is the one-sentence risk statement the approver reads first.
	RiskSummary      string   `json:"risk_summary"`
	Diff             string   `json:"diff,omitempty"`
	AffectedFeatures []string `json:"affected_features,omitempty"`
	EvidenceRefs     []string `json:"evidence_refs,omitempty"`
	// StepUp requires a person to re-confirm their identity for this
	// decision. An ambient session, or an agent using one, is not enough.
	StepUp    bool      `json:"step_up,omitempty"`
	Status    Status    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// Expired reports whether the request can no longer be decided at now.
func (r Request) Expired(now time.Time) bool { return !now.Before(r.ExpiresAt) }

// Validate checks that the request is bound, expiring and explained.
func (r Request) Validate() error {
	for _, check := range []func() error{r.FactoryID.Validate, r.SubjectDigest.Validate, r.PolicyRevision.Validate} {
		if err := check(); err != nil {
			return err
		}
	}
	if !r.IssueID.IsZero() {
		if err := r.IssueID.Validate(); err != nil {
			return err
		}
	}
	switch {
	case r.ID == "" || r.Action == "" || r.RequestedBy == "":
		return invalid("approval request needs an id, an action and a requester")
	case len(r.Nonce) < MinNonceLength:
		return invalid("approval request needs a random nonce")
	case strings.TrimSpace(r.RiskSummary) == "":
		return invalid("approval request needs a risk summary")
	case r.CreatedAt.IsZero() || !r.ExpiresAt.After(r.CreatedAt):
		return invalid("approval request must expire after it was created")
	case !r.Status.Valid():
		return invalid("approval request has an unknown status")
	}
	return nil
}

// Conditions narrow an approval. Zero fields add no condition.
type Conditions struct {
	// BudgetCap limits what the approved work may spend.
	BudgetCap core.Micros `json:"budget_cap,omitempty"`
	// ExpiresAt ends the approval earlier than the request would.
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

// Approval is one recorded decision on a request.
type Approval struct {
	RequestID string `json:"request_id"`
	// SubjectDigest, PolicyRevision and Nonce repeat the request's values;
	// the signature covers them, so the decision cannot be moved elsewhere.
	SubjectDigest  core.Digest    `json:"subject_digest"`
	PolicyRevision core.CommitSHA `json:"policy_revision"`
	Nonce          string         `json:"nonce"`
	Decision       Decision       `json:"decision"`
	// ActorID, ActorKind and Surface record who decided and where, so the
	// audit trail can tell a person from an agent.
	ActorID   string         `json:"actor_id"`
	ActorKind core.ActorKind `json:"actor_kind"`
	Surface   core.Surface   `json:"surface"`
	// StepUpAt is when the approver last re-confirmed their identity.
	StepUpAt   time.Time      `json:"step_up_at,omitzero"`
	Reason     string         `json:"reason,omitempty"`
	Conditions Conditions     `json:"conditions,omitzero"`
	DecidedAt  time.Time      `json:"decided_at"`
	ExpiresAt  time.Time      `json:"expires_at"`
	Signature  core.Signature `json:"signature,omitzero"`
}

// Validate checks that the decision is attributed, bound and dated.
func (a Approval) Validate() error {
	for _, check := range []func() error{a.SubjectDigest.Validate, a.PolicyRevision.Validate} {
		if err := check(); err != nil {
			return err
		}
	}
	switch {
	case a.RequestID == "" || a.ActorID == "" || a.Nonce == "":
		return invalid("approval needs a request, an actor and the request's nonce")
	case !a.Decision.Valid() || !a.ActorKind.Valid() || !a.Surface.Valid():
		return invalid("approval has an unknown decision, actor kind or surface")
	case a.DecidedAt.IsZero() || a.Conditions.BudgetCap < 0:
		return invalid("approval needs a decision time and a non-negative budget cap")
	}
	return nil
}

// Authorizes reports whether the approval permits acting on subject under
// policy at now. It returns PERMISSION_DENIED unless the decision is approve,
// APPROVAL_REQUIRED when the subject or the policy differs from what was
// approved, and EXPIRED once the approval or its expiry condition has lapsed.
// The signature and single use are checked by the store, not here.
func (a Approval) Authorizes(subject core.Digest, policy core.CommitSHA, now time.Time) error {
	switch {
	case a.Decision != DecisionApprove:
		return core.NewError(core.CodePermissionDenied, "the request was not approved")
	case subject == "" || policy == "" || a.SubjectDigest != subject || a.PolicyRevision != policy:
		return core.NewError(core.CodeApprovalRequired, "the approval covers a different subject or policy revision")
	case !now.Before(a.ExpiresAt) || (!a.Conditions.ExpiresAt.IsZero() && !now.Before(a.Conditions.ExpiresAt)):
		return core.NewError(core.CodeExpired, "the approval has expired")
	}
	return nil
}

// Filter selects approval requests. Zero fields do not filter.
type Filter struct {
	Statuses []Status
	IssueID  core.IssueRef
	Limit    int
}

// Store keeps approval requests and their decisions. It is the only place a
// decision becomes effective, whichever surface it arrived through.
type Store interface {
	// Create stores a valid pending request; a repeated ID is ALREADY_EXISTS.
	Create(ctx context.Context, r Request) (Request, error)
	// Get returns NOT_FOUND for an unknown ID.
	Get(ctx context.Context, id string) (Request, error)
	List(ctx context.Context, f Filter) ([]Request, error)
	// Decide records a signed decision on a pending request. The subject,
	// policy revision and nonce must equal the request's; a request already
	// decided or a foreign nonce is REPLAYED, a lapsed request EXPIRED. A
	// request marked StepUp accepts only a person with a fresh StepUpAt and
	// otherwise returns STEP_UP_REQUIRED.
	Decide(ctx context.Context, a Approval) (Request, error)
	// Decision returns the decision recorded for a request, or NOT_FOUND.
	Decision(ctx context.Context, requestID string) (Approval, error)
	// Consume spends the approval on subject under policy, atomically and
	// once. It applies Approval.Authorizes at the current time, verifies the
	// signature, and returns REPLAYED when the approval was already used.
	Consume(ctx context.Context, requestID string, subject core.Digest, policy core.CommitSHA) (Approval, error)
}

func invalid(message string) error { return core.NewError(core.CodeInvalidArgument, message) }
