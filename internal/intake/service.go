package intake

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

const (
	// MaxClarifyRounds is how many times questions are asked before an
	// issue is parked. It bounds the work an incomplete request can cause,
	// whatever the budget mode.
	MaxClarifyRounds = 3
	// DefaultApprovalTTL is how long an approval request of intake stays
	// open. It is a starting value.
	DefaultApprovalTTL = 7 * 24 * time.Hour
	// ActionApproveScope is the action an approval request of intake asks
	// for: letting one scope version of one issue go ahead.
	ActionApproveScope = "issues.approve_scope"

	// serviceActor is who intake acts as in records and requests.
	serviceActor = "service:intake"
	// defaultClarifyTimeout applies when the manifest sets no usable one.
	defaultClarifyTimeout = 72 * time.Hour
	// maxSteps bounds the lifecycle steps of one call; intake has at most
	// four edges in a row.
	maxSteps = 6
	// writeAttempts bounds how often a write that lost a race is repeated.
	writeAttempts = 3
)

// PolicySource hands out the policy in force. It returns POLICY_HOLD when
// none is, and intake then does nothing.
type PolicySource interface {
	Current(ctx context.Context) (*policy.Snapshot, error)
}

// IssueReader is the part of the GitHub client intake reads through.
type IssueReader interface {
	GetIssue(ctx context.Context, ref core.IssueRef) (github.Issue, error)
	ListComments(ctx context.Context, ref core.IssueRef, since time.Time) ([]github.Comment, error)
}

// Approvals is the approval store as intake uses it: the store contract,
// and spending an approval in the same transaction as the step it permits.
// approvals.KVStore is one.
type Approvals interface {
	approvals.Store
	ConsumeWith(ctx context.Context, requestID string, subject core.Digest, policy core.CommitSHA, write approvals.SpendFunc) (approvals.Approval, error)
}

// Config is what intake is built from. Clock and ApprovalTTL are optional.
type Config struct {
	// State holds the lifecycle records, the documents of intake and the
	// outbox its GitHub effects leave through. The approval store must keep
	// its records in the same store, so both commit together.
	State     state.Store
	Policy    PolicySource
	Issues    IssueReader
	Approvals Approvals
	Clock     core.Clock
	// ApprovalTTL overrides DefaultApprovalTTL when positive.
	ApprovalTTL time.Duration
}

// Service takes issues from intake to Ready, or to the approval inbox.
type Service struct {
	cfg Config
}

// New builds the service, refusing a configuration that lacks a part.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.State == nil || cfg.Policy == nil || cfg.Issues == nil || cfg.Approvals == nil:
		return nil, core.NewError(core.CodeInvalidArgument, "intake needs a state store, a policy source, an issue reader and an approval store")
	case cfg.ApprovalTTL < 0:
		return nil, core.NewError(core.CodeInvalidArgument, "the approval lifetime must not be negative")
	}
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	if cfg.ApprovalTTL == 0 {
		cfg.ApprovalTTL = DefaultApprovalTTL
	}
	return &Service{cfg: cfg}, nil
}

// Result says what one call did to an issue.
type Result struct {
	Issue core.IssueRef
	// State is the lifecycle state after the call, "" without a record.
	State core.State
	// Events are the lifecycle steps this call took, in order.
	Events []core.Event
	// Skipped says why the call looked no further, when it did not.
	Skipped string
}

// HandleEvent reacts to one verified webhook delivery. The event only says
// that something happened to an issue: what intake does is decided from the
// issue as GitHub shows it now, so a delivery that arrives twice or out of
// order changes nothing.
//
// Events caused by the factory's own edits are skipped. The one event read
// for itself is a person removing sisf:needs-info, which resumes a parked
// issue.
func (s *Service) HandleEvent(ctx context.Context, ev github.WebhookEvent) (Result, error) {
	if err := ev.Validate(); err != nil {
		return Result{}, err
	}
	var ref core.IssueRef
	switch {
	case ev.Issue != nil:
		ref = ev.Issue.Ref
	case ev.Comment != nil:
		ref = ev.Comment.Issue
	}
	result := Result{Issue: ref}
	switch {
	case ev.FromSelf:
		result.Skipped = "the event was caused by the factory's own edit"
	case ev.Type != "issues" && ev.Type != "issue_comment":
		result.Skipped = "the event is not about an issue"
	case ref.IsZero():
		result.Skipped = "the event names no issue"
	case ref.Owner != ev.Repo.Owner || ref.Repo != ev.Repo.Name:
		result.Skipped = "the issue is not in the repository the event came from"
	}
	if result.Skipped != "" {
		return result, nil
	}
	resume := ev.Type == "issues" && ev.Action == "unlabeled" && ev.Label != nil && ev.Label.Name == github.LabelNeedsInfo
	return s.reconcile(ctx, ref, resume)
}

// Reconcile brings one issue as far as intake can take it right now. It is
// what polling calls, and it is safe to call at any time and any number of
// times: each lifecycle step is written at most once.
func (s *Service) Reconcile(ctx context.Context, issue core.IssueRef) (Result, error) {
	return s.reconcile(ctx, issue, false)
}

func (s *Service) reconcile(ctx context.Context, ref core.IssueRef, resume bool) (Result, error) {
	result := Result{Issue: ref}
	if err := ref.Validate(); err != nil {
		return result, err
	}
	snap, err := s.load(ctx, ref)
	if err != nil {
		return result, err
	}
	result.State = snap.record.State
	// An issue that is parked or past intake is left alone before anything
	// is read from policy or GitHub: it costs nothing.
	switch {
	case snap.exists && !handles(snap.record.State):
		result.Skipped = "intake is finished for this issue"
		return result, nil
	case snap.exists && snap.doc.Clarify.NeedsInfo && !resume:
		result.Skipped = "the issue is parked until a person removes " + github.LabelNeedsInfo
		return result, nil
	}

	in, skipped, err := s.gather(ctx, ref, snap.exists)
	if err != nil || skipped != "" {
		result.Skipped = skipped
		return result, err
	}
	if resume && in.issue.HasLabel(github.LabelNeedsInfo) {
		// The label is on the issue now, whatever the event said: nobody
		// lifted the parking.
		if resume = false; snap.doc.Clarify.NeedsInfo {
			result.Skipped = "the issue is parked until a person removes " + github.LabelNeedsInfo
			return result, nil
		}
	}
	if in.comments, err = s.cfg.Issues.ListComments(ctx, ref, time.Time{}); err != nil {
		return result, err
	}
	conflicts := 0
	for range maxSteps {
		next, err := s.plan(ctx, in, snap, resume)
		if err != nil {
			return result, err
		}
		if next == nil {
			break
		}
		err = s.apply(ctx, snap, next)
		applied := err == nil
		switch {
		case applied:
			resume = false
			if next.transition != nil {
				result.Events = append(result.Events, next.transition.Event)
			}
		case core.HasCode(err, core.CodeConflict) && conflicts < writeAttempts:
			// Another writer got there first: look again and decide anew.
			conflicts++
		default:
			return result, err
		}
		if snap, err = s.load(ctx, ref); err != nil {
			return result, err
		}
		result.State = snap.record.State
		if applied && next.transition == nil {
			// The step changed no state, so nothing further follows from it.
			break
		}
	}
	return result, nil
}

// handles reports whether intake has anything to decide in state.
func handles(state core.State) bool {
	return state == core.StateIntake || state == core.StateClarifying ||
		state == core.StateScopeDraft || state == core.StateApprovalPending
}
