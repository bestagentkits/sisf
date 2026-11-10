package wiring

import (
	"context"

	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// awaiting is an issue waiting for the Owner and the request it waits on.
type awaiting struct {
	issue   core.IssueRef
	request string
}

// reconcileDecided hands back to intake every issue whose awaited approval
// request is no longer pending. A decision is made on the dashboard, the API
// or the command line and changes nothing on GitHub, so no webhook tells
// intake about it; this poll does.
//
// Only the request the issue's intake document names counts: a request it
// no longer waits on, or one filed about the issue for another action,
// neither holds the decision back nor wakes the issue. The work of a pass is
// bounded by the issues waiting: one read of each issue's document and of
// its request, and no GitHub call while the request is pending.
func (c *Control) reconcileDecided(ctx context.Context) error {
	var waiting []awaiting
	err := c.cfg.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		records, err := tx.ListRecords(ctx, state.RecordFilter{States: []core.State{core.StateApprovalPending}})
		if err != nil {
			return err
		}
		waiting = waiting[:0]
		for _, record := range records {
			id, err := intake.ReadAwaitedApproval(ctx, tx, record.IssueID)
			if err != nil && !core.HasCode(err, core.CodeNotFound) {
				return err
			}
			waiting = append(waiting, awaiting{issue: record.IssueID, request: id})
		}
		return nil
	})
	if err != nil {
		return err
	}
	var failed error
	now := c.cfg.Clock.Now()
	for _, w := range waiting {
		key := "decision:" + w.issue.String()
		if !c.due(key, now) {
			continue
		}
		if w.request != "" {
			request, err := c.Approvals.Get(ctx, w.request)
			switch {
			case err == nil && request.Status == approvals.StatusPending:
				continue
			case err != nil && !core.HasCode(err, core.CodeNotFound):
				return err
			}
		}
		// The request was decided, lapsed or is missing: intake judges which.
		if _, err := c.Intake.Reconcile(ctx, w.issue); err != nil && !permanent(err) {
			c.failed(key, now)
			failed = err
			continue
		}
		c.mu.Lock()
		delete(c.backoff, key)
		c.mu.Unlock()
	}
	return failed
}
