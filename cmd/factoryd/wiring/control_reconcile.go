package wiring

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Defaults and bounds of reconciliation with GitHub.
const (
	// DefaultReconcileInterval is how often the control zone asks GitHub
	// for the issues that changed. An idle repository answers 304 to the
	// conditional request, which costs no rate-limit budget.
	DefaultReconcileInterval = time.Minute
	MinReconcileInterval     = 10 * time.Second
	MaxReconcileInterval     = time.Hour
	// DefaultReconcileMaxPages bounds one poll to this many pages of 100
	// issues, after which the next poll continues; MaxReconcileMaxPages is
	// the GitHub client's own bound.
	DefaultReconcileMaxPages = 10
	MaxReconcileMaxPages     = 50
	// DefaultReconcileTimeout bounds one poll, rate-limit waits included,
	// so an outage of GitHub delays a pass at most this long.
	DefaultReconcileTimeout = 30 * time.Second
)

// Where the reconciliation position is kept, and the action of the inbox
// entries it stores.
const (
	reconcileNamespace = "control.reconcile"
	reconcileAction    = "reconciled"
)

// ReconcileConfig configures reconciliation with GitHub. Zero values take
// the defaults.
type ReconcileConfig struct {
	// Interval is the time between two polls.
	Interval time.Duration
	// MaxPages bounds the pages of 100 issues one poll reads.
	MaxPages int
	// Timeout bounds one poll.
	Timeout time.Duration
	// Source lists the repository's issues; nil uses the control zone's
	// GitHub client.
	Source github.IssueSource
}

// withDefaults fills in the defaults and refuses values out of bounds.
func (rc ReconcileConfig) withDefaults() (ReconcileConfig, error) {
	if rc.Interval == 0 {
		rc.Interval = DefaultReconcileInterval
	}
	if rc.MaxPages == 0 {
		rc.MaxPages = DefaultReconcileMaxPages
	}
	if rc.Timeout == 0 {
		rc.Timeout = DefaultReconcileTimeout
	}
	switch {
	case rc.Interval < MinReconcileInterval || rc.Interval > MaxReconcileInterval:
		return rc, core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("the reconciliation interval must be between %s and %s", MinReconcileInterval, MaxReconcileInterval))
	case rc.MaxPages < 1 || rc.MaxPages > MaxReconcileMaxPages:
		return rc, core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("the reconciliation page bound must be between 1 and %d", MaxReconcileMaxPages))
	case rc.Timeout < 0:
		return rc, core.NewError(core.CodeInvalidArgument, "the reconciliation timeout must be positive")
	}
	return rc, nil
}

// reconciler finds the issue changes whose webhooks never arrived. It is
// used by one control loop at a time; Control serializes the passes.
type reconciler struct {
	cfg          ReconcileConfig
	repo         github.RepoRef
	installation int64
	store        state.Store
	clock        core.Clock
	// accept stores one reconciled change; control.AcceptReconciledIssue
	// on the store.
	accept func(ctx context.Context, ev github.WebhookEvent) (bool, error)

	poller *github.Poller
	// state is the stored state, loaded with the poller.
	state reconcileState
	// next is when the next poll is due; zero means at once.
	next time.Time
	// attempted is when the last poll started, and complete when the last
	// one that read the listing to its end and stored all of it started,
	// by the factory's clock: every change on GitHub before complete is in
	// the inbox.
	attempted, complete time.Time
}

// newReconciler builds the reconciler of the factory's repository.
// confirm tells the factory's own edits from a person's change in the
// same second (see control.AcceptReconciledIssue).
func newReconciler(cfg ReconcileConfig, repo github.RepoRef, installation int64, store state.Store, clock core.Clock, confirm control.ConfirmOwnEdit) *reconciler {
	return &reconciler{cfg: cfg, repo: repo, installation: installation, store: store, clock: clock,
		accept: func(ctx context.Context, ev github.WebhookEvent) (bool, error) {
			return control.AcceptReconciledIssue(ctx, store, ev, confirm)
		}}
}

// reconcileDue polls GitHub when the interval has passed since the last
// poll, whatever its outcome, so a failing GitHub is asked once per
// interval and never on every pass.
func (c *Control) reconcileDue(ctx context.Context) error {
	if c.reconcile == nil || c.cfg.Clock.Now().Before(c.reconcile.next) {
		return nil
	}
	return c.ReconcileGitHub(ctx)
}

// ReconcileGitHub polls GitHub now for the issues of the factory's
// repository that changed since the last poll and stores every change the
// inbox does not already hold as an inbox entry, which the next drain hands
// to intake like a webhook delivery. It writes nothing to GitHub.
//
// The changes are read oldest first and the poll is bounded by the
// configured timeout and page count; a poll that stops at its bound marks
// a backlog the Owner sees in the factory's status, and the next poll
// continues where it stopped. The stored position advances only to the
// last change actually stored, so nothing is skipped:
//
//   - a failure of the store stops the poll, and the next one reads the
//     change that failed again;
//   - a change that fails validation does not hold up the others, which
//     are stored; it is read again by the next polls, up to
//     maxReconcileAttempts, and then given up, logged and listed in the
//     factory's status for the Owner.
func (c *Control) ReconcileGitHub(ctx context.Context) error {
	r := c.reconcile
	if r == nil {
		return nil
	}
	start := r.clock.Now()
	r.attempted, r.next = start, start.Add(r.cfg.Interval)
	if r.poller == nil {
		st, err := r.loadState(ctx)
		if err != nil {
			return err
		}
		r.state = st
		r.poller = github.NewPoller(r.cfg.Source, r.repo, st.Since).LimitPages(r.cfg.MaxPages)
	}
	pollCtx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()
	changes, pollErr := r.poller.Poll(pollCtx)
	if pollErr != nil {
		pollErr = core.NewError(core.CodeOf(pollErr), "reconciling with GitHub failed").WithCause(pollErr)
		if len(changes.Issues) == 0 {
			r.state.LastError = reasonOf(pollErr)
			return errors.Join(pollErr, r.saveState(ctx, r.state))
		}
	}
	now := r.clock.Now()
	stored, acked, storeErr := r.storeChanges(ctx, changes.Issues, now, c.log)
	r.poller.Ack(changes, acked)
	// A complete listing that names no retried issue any more means the
	// issue is gone; an empty answer (304, or nothing new) says nothing.
	if pollErr == nil && storeErr == nil && !changes.More && len(changes.Issues) > 0 {
		r.state.keepListed(changes.Issues)
	}
	if pollErr == nil && storeErr == nil && !changes.More && acked == len(changes.Issues) {
		r.complete = start
	}
	r.state.Since, r.state.Backlog, r.state.LastPollAt = r.poller.Since(), changes.More, now
	r.state.LastError = ""
	if err := errors.Join(pollErr, storeErr); err != nil {
		r.state.LastError = reasonOf(err)
	}
	if stored > 0 {
		c.log.Info("reconciliation found issue changes no webhook delivered", "issues", stored)
	}
	if changes.More {
		c.log.Warn("more issues changed than one reconciliation reads; the next polls continue with the rest",
			"position", r.state.Since.Format(time.RFC3339))
	}
	return errors.Join(pollErr, storeErr, r.saveState(ctx, r.state))
}

// storeChanges stores the changes of one poll, oldest first, and returns
// how many were stored and how many lead the list without a gap: the
// position may advance through those. A change that fails validation is
// counted against its bound and the rest are still stored; any other
// failure stops at the change it hit.
func (r *reconciler) storeChanges(ctx context.Context, issues []github.Issue, now time.Time, log *slog.Logger) (stored, acked int, err error) {
	acked = len(issues)
	for i := range issues {
		issue := issues[i]
		if issue.Ref.Owner != r.repo.Owner || issue.Ref.Repo != r.repo.Name {
			continue
		}
		accepted, err := r.accept(ctx, github.WebhookEvent{
			DeliveryID:     fmt.Sprintf("reconcile-%d-%d", issue.Ref.Number, issue.UpdatedAt.UnixNano()),
			Type:           "issues",
			Action:         reconcileAction,
			InstallationID: r.installation,
			Repo:           r.repo,
			Issue:          &issue,
			ReceivedAt:     now,
		})
		switch {
		case err == nil:
			r.state.stored(issue.Ref.Number)
			if accepted {
				stored++
			}
		case !core.HasCode(err, core.CodeInvalidArgument):
			return stored, min(acked, i), err
		case r.state.failed(issue, reasonOf(err)):
			log.Warn("gave up storing an issue change reconciliation found; it is listed in the factory's status",
				"issue", issue.Ref.String(), "attempts", maxReconcileAttempts, "code", string(core.CodeOf(err)))
		default:
			acked = min(acked, i)
		}
	}
	return stored, acked, nil
}
