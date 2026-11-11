package wiring

import (
	"context"
	"fmt"

	"github.com/bestagentkits/sisf/internal/intake/report"
)

// reportPublishKey is the backoff entry of report publication: an outage
// or a refused token concerns every report alike.
const reportPublishKey = "reports:publish"

// publishReports publishes a small batch of the stored bug reports as
// GitHub issues labelled report.IssueLabel, for a person to triage; none of
// them starts work. A failure leaves the reports stored and waits longer
// before each new try. An install that names the factory's account by its
// ID only publishes nothing, since an issue the factory opened could not
// be told from one someone else did, and says so once.
func (c *Control) publishReports(ctx context.Context) error {
	if c.reports == nil {
		return nil
	}
	if c.reports.Self.Login == "" {
		if !c.reportsOff {
			c.reportsOff = true
			c.log.Warn("stored bug reports are not published as issues: the manifest names the factory's GitHub account by its ID only; name its login in extensions." + extAppLogin)
		}
		return nil
	}
	now := c.cfg.Clock.Now()
	if !c.due(reportPublishKey, now) {
		return nil
	}
	res, err := c.reports.Publish(ctx)
	if res.Rejected > 0 {
		c.log.Warn("GitHub refused to open an issue for a stored bug report; after a few refusals it is kept and not tried again", "refused", res.Rejected)
	}
	if res.Unlabelled > 0 {
		c.log.Warn("GitHub shows an issue opened for a bug report without its label; the token may not label issues", "label", report.IssueLabel, "issues", res.Unlabelled)
	}
	if err != nil {
		c.failed(reportPublishKey, now)
		return fmt.Errorf("publish stored bug reports: %w", err)
	}
	c.mu.Lock()
	delete(c.backoff, reportPublishKey)
	c.mu.Unlock()
	if res.Published > 0 {
		c.log.Info("published stored bug reports as issues for triage", "published", res.Published)
	}
	return nil
}
