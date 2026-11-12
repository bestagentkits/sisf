package wiring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// IssueCloser closes or reopens an issue; github.RESTClient is one. A
// control zone whose client cannot shows the lifecycle by labels only.
type IssueCloser interface {
	SetIssueState(ctx context.Context, ref core.IssueRef, state github.IssueState) error
}

// lifecycleLabels are the labels that show where an issue is after intake.
var lifecycleLabels = []string{github.LabelRunning, github.LabelQA, github.LabelCanary, github.LabelVerified, github.LabelBlocked}

// labelOf is the lifecycle label of a state; "" for the states intake shows.
func labelOf(s core.State) string {
	switch s {
	case core.StateClaimed, core.StateDeveloping:
		return github.LabelRunning
	case core.StateQAEvaluation, core.StateReleaseCandidate:
		return github.LabelQA
	case core.StateCanary, core.StateMonitoring:
		return github.LabelCanary
	case core.StateVerified, core.StateClosed:
		return github.LabelVerified
	case core.StateRolledBack, core.StateBlocked:
		return github.LabelBlocked
	}
	return ""
}

// projectLifecycle shows each issue's lifecycle state on GitHub: one
// lifecycle label at a time, and a closed issue once the release that
// resolved it was verified. What was shown is remembered by record
// version, so an unchanged issue costs nothing; after a restart every
// issue is shown again, which changes nothing on GitHub.
func (c *Control) projectLifecycle(ctx context.Context) error {
	var records []core.StateRecord
	err := c.cfg.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		records, err = tx.ListRecords(ctx, state.RecordFilter{States: []core.State{
			core.StateClaimed, core.StateDeveloping, core.StateQAEvaluation, core.StateReleaseCandidate, core.StateCanary,
			core.StateMonitoring, core.StateVerified, core.StateClosed, core.StateRolledBack, core.StateBlocked,
		}})
		return err
	})
	if err != nil {
		return err
	}
	var failed []error
	for _, r := range records {
		c.mu.Lock()
		shown := c.projected[r.IssueID] == r.Version
		c.mu.Unlock()
		if shown {
			continue
		}
		if err := c.show(ctx, r); err != nil {
			failed = append(failed, fmt.Errorf("show %s on GitHub: %w", r.IssueID, err))
			continue
		}
		c.mu.Lock()
		c.projected[r.IssueID] = r.Version
		c.mu.Unlock()
	}
	return errors.Join(failed...)
}

// show sets the issue's lifecycle label, removes the others, and closes a
// Closed issue.
func (c *Control) show(ctx context.Context, r core.StateRecord) error {
	want := labelOf(r.State)
	if err := c.cfg.GitHub.AddLabels(ctx, r.IssueID, want); err != nil {
		return err
	}
	for _, label := range lifecycleLabels {
		if label == want {
			continue
		}
		if err := c.cfg.GitHub.RemoveLabel(ctx, r.IssueID, label); err != nil && !core.HasCode(err, core.CodeNotFound) {
			return err
		}
	}
	if closer, ok := c.cfg.GitHub.(IssueCloser); ok && r.State == core.StateClosed {
		return closer.SetIssueState(ctx, r.IssueID, github.IssueClosed)
	}
	return nil
}

// recoveryMarker identifies the recovery report comment of a run.
func recoveryMarker(run core.RunID) string { return "<!-- sisf:recovery " + string(run) + " -->" }

// deliverRecoveryReport posts the supervisor's recovery report on the
// issue whose release was rolled back. The comment carries a marker, so a
// report delivered twice shows once.
func (c *Control) deliverRecoveryReport(ctx context.Context, m state.OutboxMessage) error {
	var report release.RecoveryReport
	if err := json.Unmarshal(m.Payload, &report); err != nil || report.IssueID.Validate() != nil || report.RunID.Validate() != nil {
		return core.NewError(core.CodeInvalidArgument, "the recovery report cannot be read")
	}
	comments, err := c.cfg.GitHub.ListComments(ctx, report.IssueID, time.Time{})
	if err != nil {
		return err
	}
	marker := recoveryMarker(report.RunID)
	for _, comment := range comments {
		if strings.Contains(comment.Body, marker) {
			return nil
		}
	}
	_, err = c.cfg.GitHub.CreateComment(ctx, report.IssueID, recoveryComment(report)+"\n\n"+marker)
	return err
}

// recoveryComment is the report as the Owner reads it.
func recoveryComment(r release.RecoveryReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### Release rolled back\n\nThe release `%s` of run `%s` was rolled back to the last known good release `%s`.\n\n",
		r.Release, r.RunID, r.RestoredTo)
	fmt.Fprintf(&b, "- Reason: %s\n", oneLine(r.Reason))
	fmt.Fprintf(&b, "- Observed after the restore: `%s` on unit %s, healthy: %t\n", r.Observed.Digest, oneLine(r.Observed.Unit), r.Observed.Healthy)
	fmt.Fprintf(&b, "- Stored data untouched: %t\n\nThe issue is Blocked until the Owner retries it.", r.DataUntouched)
	return b.String()
}

// oneLine keeps a reported value on one line of plain text.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
