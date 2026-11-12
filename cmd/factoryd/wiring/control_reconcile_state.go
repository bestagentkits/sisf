package wiring

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/bestagentkits/sisf/internal/actions/runactions"
	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Bounds of the changes reconciliation could not store.
const (
	// maxReconcileAttempts is how many polls try to store a change that
	// fails validation before it is given up and shown to the Owner. Until
	// then the position stays before it, so it is read again.
	maxReconcileAttempts = 5
	// maxSkippedShown bounds the given-up changes kept for the Owner.
	maxSkippedShown = 20
)

// reconcileState is what reconciliation keeps in the store: the position a
// restarted process resumes at, and what the Owner sees of how it goes.
// A position stored before this document had more fields reads as is.
type reconcileState struct {
	// Since is where the next poll reads from, in GitHub's time: the newest
	// update stored in the inbox, or the start of a second read again.
	Since time.Time `json:"since"`
	// LastPollAt is when GitHub last answered a poll, by the factory's clock.
	LastPollAt time.Time `json:"last_poll_at,omitzero"`
	// Backlog is true when the last poll stopped at its page bound: more
	// changes wait and the next polls continue with them.
	Backlog bool `json:"backlog,omitempty"`
	// LastError is the typed reason the last poll failed; "" when it did not.
	LastError string `json:"last_error,omitempty"`
	// Retrying are changes that failed validation and are tried again.
	Retrying []reconcileFailure `json:"retrying,omitempty"`
	// Skipped are changes given up after maxReconcileAttempts polls, the
	// newest maxSkippedShown of them, oldest first.
	Skipped []reconcileFailure `json:"skipped,omitempty"`
	// ParkingDeferredSince is the earliest deadline of the unanswered
	// issues whose parking waits until every answer is known to be taken
	// in, and ParkingDeferredReason why; zero when none waits.
	ParkingDeferredSince  time.Time `json:"parking_deferred_since,omitzero"`
	ParkingDeferredReason string    `json:"parking_deferred_reason,omitempty"`
}

// reconcileFailure is one change of an issue that could not be stored.
type reconcileFailure struct {
	Issue     int       `json:"issue"`
	UpdatedAt time.Time `json:"updated_at"`
	Attempts  int       `json:"attempts"`
	Reason    string    `json:"reason"`
}

// failed counts one more failed attempt to store issue's change and reports
// whether the change is given up now. A new update of the issue starts the
// count again: it is another change.
func (s *reconcileState) failed(issue github.Issue, reason string) bool {
	i := slices.IndexFunc(s.Retrying, func(f reconcileFailure) bool { return f.Issue == issue.Ref.Number })
	if i < 0 {
		s.Retrying = append(s.Retrying, reconcileFailure{Issue: issue.Ref.Number, UpdatedAt: issue.UpdatedAt})
		i = len(s.Retrying) - 1
	}
	f := &s.Retrying[i]
	if !f.UpdatedAt.Equal(issue.UpdatedAt) {
		f.UpdatedAt, f.Attempts = issue.UpdatedAt, 0
	}
	f.Attempts++
	f.Reason = reason
	if f.Attempts < maxReconcileAttempts {
		return false
	}
	s.Skipped = append(s.Skipped, *f)
	if len(s.Skipped) > maxSkippedShown {
		s.Skipped = s.Skipped[len(s.Skipped)-maxSkippedShown:]
	}
	s.Retrying = slices.Delete(s.Retrying, i, i+1)
	return true
}

// stored forgets the failures of an issue whose change was stored.
func (s *reconcileState) stored(number int) {
	s.Retrying = slices.DeleteFunc(s.Retrying, func(f reconcileFailure) bool { return f.Issue == number })
}

// keepListed forgets the failures of issues a complete poll no longer
// lists: they were deleted or moved away, and nothing is left to retry.
func (s *reconcileState) keepListed(listed []github.Issue) {
	s.Retrying = slices.DeleteFunc(s.Retrying, func(f reconcileFailure) bool {
		return !slices.ContainsFunc(listed, func(is github.Issue) bool { return is.Ref.Number == f.Issue })
	})
}

// loadState reads the stored state; a factory that never polled starts
// from the beginning, which reads its repository's issues once.
func (r *reconciler) loadState(ctx context.Context) (reconcileState, error) {
	var st reconcileState
	err := r.store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, reconcileNamespace, r.key())
		if core.HasCode(err, core.CodeNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal(entry.Value, &st); err != nil {
			return core.NewError(core.CodeInternal, "the stored reconciliation position is unreadable").WithCause(err)
		}
		return nil
	})
	return st, err
}

// saveState stores st; its position is where a restarted process resumes.
func (r *reconciler) saveState(ctx context.Context, st reconcileState) error {
	value, err := json.Marshal(st)
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

// recordParkingDeferred stores the deferral of parking (see
// Control.noteParkingDeferred) when the stored state differs, and keeps it
// in the state the reconciler holds, which its next save writes whole.
func (r *reconciler) recordParkingDeferred(ctx context.Context, since time.Time, why string) error {
	st, err := r.loadState(ctx)
	if err != nil {
		return err
	}
	if !st.ParkingDeferredSince.Equal(since) || st.ParkingDeferredReason != why {
		st.ParkingDeferredSince, st.ParkingDeferredReason = since, why
		if err := r.saveState(ctx, st); err != nil {
			return err
		}
	}
	r.state.ParkingDeferredSince, r.state.ParkingDeferredReason = since, why
	return nil
}

// key names the position of the factory's one repository.
func (r *reconciler) key() string { return "github/" + r.repo.Owner + "/" + r.repo.Name }

// GitHubSync reports how the factory keeps up with GitHub, for its status:
// the stored state of reconciliation and, on an install acting with the
// operator's token, that token's access, and how the publication of bug
// reports stands. It reads the store, so it shows what the running control
// zone last stored.
func (c *Control) GitHubSync(ctx context.Context) (runactions.GitHubSync, error) {
	var out runactions.GitHubSync
	if c.reconcile != nil {
		st, err := c.reconcile.loadState(ctx)
		if err != nil {
			return out, err
		}
		out.Position, out.LastPollAt, out.Backlog, out.LastError = st.Since, st.LastPollAt, st.Backlog, st.LastError
		out.Retrying, out.Skipped = syncIssues(st.Retrying), syncIssues(st.Skipped)
		out.ParkingDeferredSince, out.ParkingDeferredReason = st.ParkingDeferredSince, st.ParkingDeferredReason
	}
	if c.access != nil {
		c.access.status(&out)
	}
	if c.reports != nil {
		published, err := control.ReadReportPublication(ctx, c.cfg.Store, c.cfg.Clock.Now())
		if err != nil {
			return out, err
		}
		out.ReportsWaiting, out.ReportsFailed, out.ReportPublishError = published.Waiting, published.Failed, published.LastError
		out.ReportsPublishPausedUntil = published.PausedUntil
		out.ReportsDropped = published.Dropped
	}
	return out, nil
}

// syncIssues shows failures as the factory's status lists them.
func syncIssues(failures []reconcileFailure) []runactions.SyncIssue {
	var out []runactions.SyncIssue
	for _, f := range failures {
		out = append(out, runactions.SyncIssue{Issue: f.Issue, UpdatedAt: f.UpdatedAt, Attempts: f.Attempts, Reason: f.Reason})
	}
	return out
}
