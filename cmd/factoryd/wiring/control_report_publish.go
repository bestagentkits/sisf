package wiring

import (
	"context"
	"fmt"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/pkg/core"
)

// reportPublishKey is the backoff entry of report publication: an outage
// or a refused token concerns every report alike.
const reportPublishKey = "reports:publish"

// reportPublishSpacing is the least time between two publications. The
// control pass runs every second, and the factory's token also writes its
// own comments and labels, so with control.DefaultReportPublishBatch per
// publication at most 300 report issues are opened an hour, below GitHub's
// limit of 500 content-creating requests an hour, and a full store of
// 1,000 reports drains in under four hours.
const reportPublishSpacing = time.Minute

// publishReports publishes a small batch of the stored bug reports as
// GitHub issues labelled report.IssueLabel, for a person to triage; none of
// them starts work. A failure leaves the reports stored and waits longer
// before each new try. The factory's account is learned from GitHub as the
// progress comments learn it when the install names none; an install that
// names it by its ID only publishes nothing, since an issue the factory
// opened could not be told from one someone else did, and says so once.
func (c *Control) publishReports(ctx context.Context) error {
	if c.reports == nil {
		return nil
	}
	now := c.cfg.Clock.Now()
	if !c.due(reportPublishKey, now) {
		return nil
	}
	publisher := *c.reports
	if publisher.Self.IsZero() {
		writer, err := c.progressWriter(ctx)
		switch {
		case core.HasCode(err, core.CodeNotSupported):
			c.reportsOffOnce("stored bug reports are not published as issues: the factory's GitHub account cannot be learned; name its login in extensions." + extAppLogin)
			return nil
		case err != nil:
			c.reportPublishFailed(now)
			return fmt.Errorf("learn the factory's GitHub account to publish bug reports: %w", err)
		}
		publisher.Self = writer.Self
	}
	if publisher.Self.Login == "" {
		c.reportsOffOnce("stored bug reports are not published as issues: the manifest names the factory's GitHub account by its ID only; name its login in extensions." + extAppLogin)
		return nil
	}
	res, err := publisher.Publish(ctx)
	if res.Rejected > 0 {
		c.log.Warn("GitHub refused to open an issue for a stored bug report; after a few refusals it is kept and not tried again", "refused", res.Rejected)
	}
	if res.Unlabelled > 0 {
		c.log.Warn("GitHub shows an issue opened for a bug report without its label; the token may not label issues", "label", report.IssueLabel, "issues", res.Unlabelled)
	}
	if err != nil {
		c.reportPublishFailed(now)
		return fmt.Errorf("publish stored bug reports: %w", err)
	}
	c.mu.Lock()
	c.backoff[reportPublishKey] = retryAt{at: now.Add(reportPublishSpacing)}
	c.mu.Unlock()
	if res.Published > 0 {
		c.log.Info("published stored bug reports as issues for triage", "published", res.Published)
	}
	return nil
}

// reportPublishFailed schedules the next publication after a failure: the
// growing wait of a failed delivery, and never sooner than the spacing.
func (c *Control) reportPublishFailed(now time.Time) {
	c.failed(reportPublishKey, now)
	c.mu.Lock()
	defer c.mu.Unlock()
	next := c.backoff[reportPublishKey]
	next.at = maxTime(next.at, now.Add(reportPublishSpacing))
	c.backoff[reportPublishKey] = next
}

// reportsOffOnce says once why no report is published.
func (c *Control) reportsOffOnce(why string) {
	if !c.reportsOff {
		c.reportsOff = true
		c.log.Warn(why)
	}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
