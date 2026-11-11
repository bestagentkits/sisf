package wiring

import (
	"context"
	"encoding/json"
	"fmt"
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
	// issues; MaxReconcileMaxPages is the GitHub client's own bound.
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

	poller *github.Poller
	// next is when the next poll is due; zero means at once.
	next time.Time
}

// reconcilePosition is the stored position: the newest update GitHub
// reported, in GitHub's own time.
type reconcilePosition struct {
	Since time.Time `json:"since"`
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
// The poll is bounded by the configured timeout and page count. A listing
// longer than the page bound cannot be read without skipping changes, so
// the position moves to one interval before now and the skipped window is
// logged; webhooks and redeliveries remain the primary path.
func (c *Control) ReconcileGitHub(ctx context.Context) error {
	r := c.reconcile
	if r == nil {
		return nil
	}
	r.next = r.clock.Now().Add(r.cfg.Interval)
	if r.poller == nil {
		since, err := r.position(ctx)
		if err != nil {
			return err
		}
		r.poller = github.NewPoller(r.cfg.Source, r.repo, since).LimitPages(r.cfg.MaxPages)
	}
	pollCtx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()
	before := r.poller.Since()
	changed, err := r.poller.Poll(pollCtx)
	if core.HasCode(err, core.CodeLimitExceeded) {
		skipTo := r.clock.Now().Add(-r.cfg.Interval).UTC()
		c.log.Warn("too many issues changed to reconcile; older changes are left to webhooks and redeliveries",
			"since", before.Format(time.RFC3339), "resume_at", skipTo.Format(time.RFC3339))
		r.poller = nil
		return r.savePosition(ctx, skipTo)
	}
	if err != nil {
		return core.NewError(core.CodeOf(err), "reconciling with GitHub failed").WithCause(err)
	}
	now := r.clock.Now()
	stored := 0
	for _, issue := range changed {
		if issue.Ref.Owner != r.repo.Owner || issue.Ref.Repo != r.repo.Name {
			continue
		}
		ev := github.WebhookEvent{
			DeliveryID:     fmt.Sprintf("reconcile-%d-%d", issue.Ref.Number, issue.UpdatedAt.UnixNano()),
			Type:           "issues",
			Action:         reconcileAction,
			InstallationID: r.installation,
			Repo:           r.repo,
			Issue:          &issue,
			ReceivedAt:     now,
		}
		accepted, err := control.AcceptReconciledIssue(ctx, r.store, ev)
		if err != nil {
			// The position is not saved, so the next poll of a restarted
			// process reads this change again.
			return err
		}
		if accepted {
			stored++
		}
	}
	if stored > 0 {
		c.log.Info("reconciliation found issue changes no webhook delivered", "issues", stored)
	}
	if since := r.poller.Since(); !since.Equal(before) {
		return r.savePosition(ctx, since)
	}
	return nil
}

// position reads the stored position; a factory that never polled starts
// from the beginning, which reads its repository's issues once.
func (r *reconciler) position(ctx context.Context) (time.Time, error) {
	var pos reconcilePosition
	err := r.store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, reconcileNamespace, r.key())
		if core.HasCode(err, core.CodeNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal(entry.Value, &pos); err != nil {
			return core.NewError(core.CodeInternal, "the stored reconciliation position is unreadable").WithCause(err)
		}
		return nil
	})
	return pos.Since, err
}

// savePosition stores since as the position a restarted process resumes at.
func (r *reconciler) savePosition(ctx context.Context, since time.Time) error {
	value, err := json.Marshal(reconcilePosition{Since: since})
	if err != nil {
		return core.NewError(core.CodeInternal, "the reconciliation position could not be encoded").WithCause(err)
	}
	return r.store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		var version int64
		entry, err := tx.GetKV(ctx, reconcileNamespace, r.key())
		switch {
		case err == nil:
			version = entry.Version
		case !core.HasCode(err, core.CodeNotFound):
			return err
		}
		_, err = tx.PutKV(ctx, state.KVEntry{Namespace: reconcileNamespace, Key: r.key(), Value: value, Version: version})
		return err
	})
}

// key names the position of the factory's one repository.
func (r *reconciler) key() string { return "github/" + r.repo.Owner + "/" + r.repo.Name }
