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

// defaultPollRequests bounds the requests of one Poll that set no bound.
const defaultPollRequests = 50

// Poller reconciles with GitHub when webhooks may have been lost. Each Poll
// asks only for issues updated since the position, oldest first, and sends
// the ETag of the previous answer so an idle repository costs a 304 and no
// rate-limit budget.
//
// It is the safety net under webhooks, not a second source of truth: it
// returns what changed, and the caller feeds it through the same inbox as a
// webhook, which deduplicates. The position moves only when the caller
// acknowledges what it stored (Ack), so a change the caller failed to store
// is read again by the next Poll.
type Poller struct {
	src      IssueSource
	repo     RepoRef
	labels   []string
	maxPages int

	mu sync.Mutex
	// since and page are the position: the next poll asks for the listing
	// from since at page. The page is 1 except while more updates within
	// since's second than a page holds are stepped through.
	since    time.Time
	page     int
	etag     string
	etagFor  time.Time
	lastSeen map[int]time.Time
	walk     pageWalk
}

// Changes is what one Poll found. Hand it back to Ack once its issues
// were stored.
type Changes struct {
	// Issues are the changes not reported before, oldest update first.
	Issues []Issue
	// More is true when the poll stopped before the end of the listing; the
	// next Poll continues after what was acknowledged.
	More bool

	// from is the position the poll started at; next is where the listing
	// continues after everything it read.
	from, next listPosition
	// readAt is the position of the request that listed each issue.
	readAt []listPosition
	// etag is the ETag of a listing read whole in one request at from.
	etag string
	// walk is the page walk once everything read was stored.
	walk pageWalk
}

// NewPoller starts polling at since. A zero since reads everything, oldest
// first, which is how a fresh factory reconciles. labels, if any, restrict
// the listing to issues carrying all of them.
func NewPoller(src IssueSource, repo RepoRef, since time.Time, labels ...string) *Poller {
	return &Poller{src: src, repo: repo, since: since, page: 1, labels: labels, lastSeen: map[int]time.Time{}}
}

// LimitPages bounds the pages of 100 issues one Poll steps through; a
// listing that needs more is cut there and reported as Changes.More. Zero
// means 50. A step through one second by page also reads the page before
// it again (see pageWalk), which the bound does not count, so every Poll
// gets at least one page further. Call it before the first Poll.
func (p *Poller) LimitPages(n int) *Poller {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maxPages = n
	return p
}

// Since is the position to persist, so a restart resumes without a gap.
// A poller started at it reads its second from the first page: what pages
// of that second were read is not kept, so a restart reads them again
// rather than risk skipping an issue that moved between them.
func (p *Poller) Since() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.since
}

// Poll returns the issues that changed since the position, oldest update
// first, and moves nothing: the position advances only through Ack.
//
// It reads page by page, each request continuing from the newest update the
// previous one returned, so an issue that changes while the poll runs
// cannot shift another out of the pages read. Only a full page of updates
// within one second is stepped through by page number, and the page is kept
// with the position, so the next poll goes on where this one stopped (see
// pageWalk for how every step of that walk is checked). A failure after
// some pages were read returns what was read, marked More, with the error.
func (p *Poller) Poll(ctx context.Context) (Changes, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	at := listPosition{since: p.since, page: max(p.page, 1)}
	out := Changes{from: at, next: at}
	limit := p.maxPages
	if limit <= 0 {
		limit = defaultPollRequests
	}
	reported := map[int]time.Time{}
	report := func(at listPosition, issues []Issue) {
		for _, issue := range issues {
			// GitHub's since is inclusive and has a precision of one
			// second, so updates already reported come back; each is
			// reported once.
			if seen, ok := p.lastSeen[issue.Ref.Number]; ok && !issue.UpdatedAt.After(seen) {
				continue
			}
			if seen, ok := reported[issue.Ref.Number]; ok && !issue.UpdatedAt.After(seen) {
				continue
			}
			reported[issue.Ref.Number] = issue.UpdatedAt
			out.Issues = append(out.Issues, issue)
			out.readAt = append(out.readAt, at)
		}
	}
	walk := p.walk.clone()
	out.walk = walk
	for request := 0; ; request++ {
		if request == limit {
			out.More = true
			return out, nil
		}
		etag := ""
		if request == 0 && at.page == 1 && p.etag != "" && p.etagFor.Equal(p.since) {
			etag = p.etag
		}
		got, err := p.src.PollIssues(ctx, p.repo, ListIssuesOptions{Since: at.since, Labels: p.labels, MaxPages: 1, Page: at.page}, etag)
		var skipped []backfill
		if err == nil && !got.NotModified && at.page > 1 {
			skipped, err = p.coverStep(ctx, walk, at)
		}
		if err != nil {
			if request == 0 {
				return Changes{}, err
			}
			out.More = true
			return out, err
		}
		if got.NotModified {
			return out, nil
		}
		// What the step skipped comes before the page, oldest first.
		for i := len(skipped) - 1; i >= 0; i-- {
			report(skipped[i].at, skipped[i].got.Issues)
			walk.include(at.since, entries(skipped[i].got))
		}
		report(at, got.Issues)
		walk.record(at, entries(got))
		out.walk = walk.clone()
		at = at.after(got)
		out.next = at
		if !got.More {
			if request == 0 && out.from.page == 1 {
				out.etag = got.ETag
			}
			return out, nil
		}
	}
}

// Ack records that the first n issues of c were stored. When every issue of
// c was stored, the position moves past everything the poll read, or back
// to the first page of a second an issue left while it was walked by page.
// Otherwise it moves to the newest issue stored, and the issue not
// acknowledged, and everything after it, is read again by the next Poll.
func (p *Poller) Ack(c Changes, n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n = max(0, min(n, len(c.Issues)))
	for _, issue := range c.Issues[:n] {
		if seen, ok := p.lastSeen[issue.Ref.Number]; !ok || issue.UpdatedAt.After(seen) {
			p.lastSeen[issue.Ref.Number] = issue.UpdatedAt
		}
	}
	complete := n == len(c.Issues) && !c.More
	switch {
	case n == len(c.Issues):
		// The walk moves on only with the position: after a poll stored in
		// part, the next step is checked against the walk as it was.
		p.since, p.page = c.next.since, c.next.page
		p.walk = c.walk
	case n > 0:
		// The issue not stored is read again from the request that
		// listed it, or from the newest update stored when that is in a
		// later second.
		to := listPosition{since: c.Issues[n-1].UpdatedAt, page: 1}
		if retry := c.readAt[n]; !to.since.Truncate(time.Second).After(retry.since.Truncate(time.Second)) {
			to = retry
		}
		p.since, p.page = to.since, to.page
	}
	// An update older than the second the next request starts at is never
	// listed again.
	floor := p.since.Truncate(time.Second)
	for number, at := range p.lastSeen {
		if at.Before(floor) {
			delete(p.lastSeen, number)
		}
	}
	// The ETag stands for a listing read whole at a position; it is sent
	// again only when the next poll asks from that same position.
	if complete && c.etag != "" {
		p.etag, p.etagFor = c.etag, c.from.since
	}
}
