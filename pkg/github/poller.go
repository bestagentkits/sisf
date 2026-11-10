package github

import (
	"context"
	"sync"
	"time"
)

// IssueSource lists issues conditionally. RESTClient is one.
type IssueSource interface {
	PollIssues(ctx context.Context, repo RepoRef, opts ListIssuesOptions, etag string) (IssuePage, error)
}

var _ IssueSource = (*RESTClient)(nil)

// Poller reconciles with GitHub when webhooks may have been lost. Each Poll
// asks only for issues updated since the last one, and sends the ETag of the
// previous answer so an idle repository costs a 304 and no rate-limit budget.
//
// It is the safety net under webhooks, not a second source of truth: it
// returns what changed, and the caller feeds it through the same inbox as a
// webhook, which deduplicates.
type Poller struct {
	src    IssueSource
	repo   RepoRef
	labels []string

	mu       sync.Mutex
	since    time.Time
	etag     string
	etagFor  time.Time
	lastSeen map[int]time.Time
}

// NewPoller starts polling at since. A zero since reads everything once,
// which is how a fresh factory reconciles. labels, if any, restrict the
// listing to issues carrying all of them.
func NewPoller(src IssueSource, repo RepoRef, since time.Time, labels ...string) *Poller {
	return &Poller{src: src, repo: repo, since: since, labels: labels, lastSeen: map[int]time.Time{}}
}

// Since is the position to persist, so a restart resumes without a gap.
func (p *Poller) Since() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.since
}

// Poll returns the issues that changed since the previous call, oldest
// update first. It advances only after GitHub answered, so a failed poll is
// simply repeated. The position is the newest update seen, not the wall
// clock, so an update that lands while a poll runs is never skipped.
func (p *Poller) Poll(ctx context.Context) ([]Issue, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	etag := ""
	if p.etagFor.Equal(p.since) {
		etag = p.etag
	}
	page, err := p.src.PollIssues(ctx, p.repo, ListIssuesOptions{Since: p.since, Labels: p.labels}, etag)
	if err != nil {
		return nil, err
	}
	if page.NotModified {
		return nil, nil
	}
	var changed []Issue
	for _, issue := range page.Issues {
		// GitHub's since is inclusive, so the newest issue comes back once
		// more; an update already reported is not reported again.
		if seen, ok := p.lastSeen[issue.Ref.Number]; ok && !issue.UpdatedAt.After(seen) {
			continue
		}
		p.lastSeen[issue.Ref.Number] = issue.UpdatedAt
		changed = append(changed, issue)
		if issue.UpdatedAt.After(p.since) {
			p.since = issue.UpdatedAt
		}
	}
	for number, at := range p.lastSeen {
		if at.Before(p.since) {
			delete(p.lastSeen, number)
		}
	}
	p.etag, p.etagFor = page.ETag, p.since
	return changed, nil
}
