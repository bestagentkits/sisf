package wiring

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// reconcileDecided hands back to intake every issue that waits for an
// approval which is no longer pending. A decision is made on the dashboard,
// the API or the command line and changes nothing on GitHub, so no webhook
// tells intake about it; this poll does. An issue whose request is still
// pending costs one store read and no GitHub call.
func (c *Control) reconcileDecided(ctx context.Context) error {
	var waiting []core.StateRecord
	err := c.cfg.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		waiting, err = tx.ListRecords(ctx, state.RecordFilter{States: []core.State{core.StateApprovalPending}})
		return err
	})
	if err != nil {
		return err
	}
	var failed error
	now := c.cfg.Clock.Now()
	for _, record := range waiting {
		key := "decision:" + record.IssueID.String()
		if !c.due(key, now) {
			continue
		}
		pending, err := c.Approvals.List(ctx, approvals.Filter{IssueID: record.IssueID,
			Statuses: []approvals.Status{approvals.StatusPending}, Limit: 1})
		if err != nil {
			return err
		}
		if len(pending) > 0 {
			continue
		}
		if _, err := c.Intake.Reconcile(ctx, record.IssueID); err != nil && !permanent(err) {
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
