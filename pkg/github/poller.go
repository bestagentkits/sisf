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

	mu       sync.Mutex
	since    time.Time
	etag     string
	etagFor  time.Time
	lastSeen map[int]time.Time
}

// Changes is what one Poll found. Hand it back to Ack once its issues
// were stored.
type Changes struct {
	// Issues are the changes not reported before, oldest update first.
	Issues []Issue
	// More is true when the poll stopped at its page bound before the end
	// of the listing; the next Poll continues after what was acknowledged.
	More bool

	// from is the position the poll started at, through the newest update
	// it read, pull requests included.
	from, through time.Time
	// etag is the ETag of a listing read whole in one request at from.
	etag string
}

// NewPoller starts polling at since. A zero since reads everything, oldest
// first, which is how a fresh factory reconciles. labels, if any, restrict
// the listing to issues carrying all of them.
func NewPoller(src IssueSource, repo RepoRef, since time.Time, labels ...string) *Poller {
	return &Poller{src: src, repo: repo, since: since, labels: labels, lastSeen: map[int]time.Time{}}
}

// LimitPages bounds the requests of 100 issues one Poll makes; a listing
// that needs more is cut there and reported as Changes.More. Zero means
// 50. Call it before the first Poll.
func (p *Poller) LimitPages(n int) *Poller {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maxPages = n
	return p
}

// Since is the position to persist, so a restart resumes without a gap.
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
// within one second is stepped through by page number. A failure after some
// pages were read returns what was read, marked More, with the error.
func (p *Poller) Poll(ctx context.Context) (Changes, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := Changes{from: p.since, through: p.since}
	limit := p.maxPages
	if limit <= 0 {
		limit = defaultPollRequests
	}
	cursor, page := p.since, 1
	reported := map[int]time.Time{}
	for request := 0; ; request++ {
		if request == limit {
			out.More = true
			return out, nil
		}
		etag := ""
		if request == 0 && p.etag != "" && p.etagFor.Equal(p.since) {
			etag = p.etag
		}
		got, err := p.src.PollIssues(ctx, p.repo, ListIssuesOptions{Since: cursor, Labels: p.labels, MaxPages: 1, Page: page}, etag)
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
		for _, issue := range got.Issues {
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
		}
		if got.Through.After(out.through) {
			out.through = got.Through
		}
		if !got.More {
			if request == 0 {
				out.etag = got.ETag
			}
			return out, nil
		}
		if got.Through.Truncate(time.Second).After(cursor.Truncate(time.Second)) {
			cursor, page = got.Through, 1
		} else {
			page++
		}
	}
}

// Ack records that the first n issues of c were stored. The position moves
// to the newest of them, or, when every issue of c was stored, through
// everything the poll read; an issue not acknowledged, and everything after
// it, is read again by the next Poll.
func (p *Poller) Ack(c Changes, n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n = max(0, min(n, len(c.Issues)))
	for _, issue := range c.Issues[:n] {
		if seen, ok := p.lastSeen[issue.Ref.Number]; !ok || issue.UpdatedAt.After(seen) {
			p.lastSeen[issue.Ref.Number] = issue.UpdatedAt
		}
	}
	to := c.through
	if n < len(c.Issues) {
		to = c.from
		if n > 0 {
			to = c.Issues[n-1].UpdatedAt
		}
	}
	if to.After(p.since) {
		p.since = to
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
	if n == len(c.Issues) && !c.More && c.etag != "" {
		p.etag, p.etagFor = c.etag, c.from
	}
}
