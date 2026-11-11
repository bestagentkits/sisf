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
// spending an approval in the same transaction as the step it permits, and
// closing a request that a newer one replaced. approvals.KVStore is one.
type Approvals interface {
	approvals.Store
	ConsumeWith(ctx context.Context, requestID string, subject core.Digest, policy core.CommitSHA, write approvals.SpendFunc) (approvals.Approval, error)
	Supersede(ctx context.Context, id, replacement string) (approvals.Request, error)
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
// Events caused by the factory's own edits are skipped. A parked issue
// resumes when a person removed sisf:needs-info: the webhook of that
// removal says so, and so does any other event, a reconciled change found
// by polling included, whose issue no longer carries the label. Either way
// that counts only once the factory's own label of the current parking was
// confirmed on GitHub: before, a removal webhook may be a late one of an
// earlier parking, and an issue seen without the label was not lifted by
// anyone. The issue is then read again and resumes only if the label is
// gone now; an event whose issue still shows the label costs nothing.
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
	resume := ev.Type == "issues" && ev.Action == "unlabeled" && ev.Label != nil && sameLabel(ev.Label.Name, github.LabelNeedsInfo)
	return s.reconcile(ctx, ref, resume, ev.Issue)
}

// Reconcile brings one issue as far as intake can take it right now. It is
// what polling calls, and it is safe to call at any time and any number of
// times: each lifecycle step is written at most once.
func (s *Service) Reconcile(ctx context.Context, issue core.IssueRef) (Result, error) {
	return s.reconcile(ctx, issue, false, nil)
}

// liftedIn reports whether shown, the issue as an event carried it, says
// that a person removed the label of a parking the factory confirmed on
// GitHub. It is a reason to read the issue again, not proof.
func liftedIn(shown *github.Issue, ref core.IssueRef, doc issueDoc) bool {
	return shown != nil && shown.Ref == ref && doc.Clarify.LabelConfirmed && !HasLabel(shown.Labels, github.LabelNeedsInfo)
}

// unconfirmedParking is why an issue parked with a label not confirmed on
// GitHub is left parked.
const unconfirmedParking = "the issue is parked and its " + github.LabelNeedsInfo + " label is not confirmed on GitHub yet"

// confirmStoredParking looks once at an issue parked before the factory
// recorded whether its label reached GitHub. Seen carrying the label, the
// parking is confirmed; seen without it, nothing tells a label still
// queued from one a person removed, so it stays parked until its queued
// label is delivered or the label is seen on the issue.
func (s *Service) confirmStoredParking(ctx context.Context, ref core.IssueRef, snap snapshot) error {
	issue, err := s.cfg.Issues.GetIssue(ctx, ref)
	if err != nil || !HasLabel(issue.Labels, github.LabelNeedsInfo) {
		return err
	}
	doc := snap.doc
	doc.Clarify.LabelPending, doc.Clarify.LabelConfirmed = false, true
	return s.apply(ctx, snap, &plan{doc: doc})
}

func (s *Service) reconcile(ctx context.Context, ref core.IssueRef, resume bool, shown *github.Issue) (Result, error) {
	result := Result{Issue: ref}
	if err := ref.Validate(); err != nil {
		return result, err
	}
	snap, err := s.load(ctx, ref)
	if err != nil {
		return result, err
	}
	if len(snap.doc.Closing) > 0 {
		// A request a written step replaced is closed before anything else,
		// whatever the issue's state: closing it once failed.
		if err := s.closeReplaced(ctx, snap.doc); err != nil {
			return result, err
		}
		if snap, err = s.load(ctx, ref); err != nil {
			return result, err
		}
	}
	result.State = snap.record.State
	// An issue that is parked or past intake is left alone before anything
	// is read from policy or GitHub: it costs nothing.
	switch {
	case snap.exists && !handles(snap.record.State):
		result.Skipped = "intake is finished for this issue"
		return result, nil
	case snap.exists && snap.doc.Clarify.NeedsInfo && !snap.doc.Clarify.LabelConfirmed:
		result.Skipped = unconfirmedParking
		if snap.doc.Clarify.LabelPending {
			return result, nil
		}
		return result, s.confirmStoredParking(ctx, ref, snap)
	case snap.exists && snap.doc.Clarify.NeedsInfo && !resume && !liftedIn(shown, ref, snap.doc):
		result.Skipped = "the issue is parked until a person removes " + github.LabelNeedsInfo
		return result, nil
	case snap.exists && snap.doc.Clarify.NeedsInfo:
		// Whatever the event, the issue read below decides.
		resume = true
	}

	in, skipped, err := s.gather(ctx, ref, snap.exists)
	if err != nil || skipped != "" {
		result.Skipped = skipped
		return result, err
	}
	if resume && HasLabel(in.issue.Labels, github.LabelNeedsInfo) {
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
