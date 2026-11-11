package github

import (
	"context"
	"maps"
	"time"
)

// pageWalk keeps a walk through updates sharing one second from skipping
// an entry of the listing.
//
// GitHub's since has a precision of one second, so a poller steps through
// more updates within one second than a page holds by page number, and a
// page number is an offset into the listing. Entries only ever leave a
// second that is over: an issue or pull request that changes moves to the
// end of the listing, and one that is deleted, transferred or loses a label
// the poller filters on leaves it altogether; none enters a past second,
// since every change sets the update to the present. Each entry that leaves
// a page already read moves the entries after it back one place, so the
// first entries of the next page land on a page already read and would
// never be listed again.
//
// So every step of a walk is checked: after reading page p, the poller
// reads page p-1 again. When it holds exactly the entries it held when it
// was read, nothing before the end of page p-1 left by the time page p was
// read, and page p continued it without a gap. Otherwise the page read again
// holds what was skipped, provided it also holds an entry the walk read
// before (see coverStep), since the entries of a listing keep their order
// and those not read yet all come after those read. A step is checked
// against the walk as last acknowledged, so a poll that is not stored, or
// stored only in part, is checked against what is known for sure.
//
// The check costs one request per page of the walk, and the pages it reads
// again are reported once (Poller.lastSeen), or deduplicated by the
// caller's inbox. A walk is not sent back to its first page by changes, so
// it ends however much its second keeps changing.
type pageWalk struct {
	// since is the position the walk lists from; zero when there is no walk.
	since time.Time
	// page is the page last read, whose entries are last.
	page int
	last map[entry]bool
	// seen are the entries the walk read.
	seen map[entry]bool
}

// entry is a place in the listing: an issue or pull request as of one
// update.
type entry struct {
	number  int
	updated time.Time
}

// entries are the entries of a page, pull requests included, as the
// listing holds them.
func entries(got IssuePage) map[entry]bool {
	out := make(map[entry]bool, len(got.Issues)+len(got.pulls))
	for _, list := range [][]Issue{got.Issues, got.pulls} {
		for _, is := range list {
			out[entry{is.Ref.Number, is.UpdatedAt}] = true
		}
	}
	return out
}

// clone copies the walk, so a poll can advance it without changing the walk
// as last acknowledged.
func (w pageWalk) clone() pageWalk {
	w.last, w.seen = maps.Clone(w.last), maps.Clone(w.seen)
	return w
}

// knows reports whether the walk read the page before at as the last.
func (w pageWalk) knows(at listPosition) bool {
	return w.since.Equal(at.since) && w.page == at.page-1 && w.last != nil
}

// record takes in the page read at at as the last page of the walk.
func (w *pageWalk) record(at listPosition, read map[entry]bool) {
	w.include(at.since, read)
	w.page, w.last = at.page, read
}

// include adds entries read from since to those the walk read. A new
// position starts a new walk.
func (w *pageWalk) include(since time.Time, read map[entry]bool) {
	if !w.since.Equal(since) || w.seen == nil {
		*w = pageWalk{since: since, seen: map[entry]bool{}}
	}
	maps.Copy(w.seen, read)
}

// backfill is a page read again because a step of a walk skipped entries.
type backfill struct {
	at  listPosition
	got IssuePage
}

// coverStep checks the step of the walk w to the page at at, which was just
// read, and returns the pages that hold what the step skipped: none when
// the page before at holds what it held when it was read. Otherwise pages
// are read again backwards from the one before at until one holds an entry
// the walk read before, or is the first: the entries skipped come after
// every entry read before and before the first entry of the page at at, so
// they lie on the pages read again.
func (p *Poller) coverStep(ctx context.Context, w pageWalk, at listPosition) ([]backfill, error) {
	var seen map[entry]bool
	if w.since.Equal(at.since) {
		seen = w.seen
	}
	var out []backfill
	for page := at.page - 1; page >= 1; page-- {
		pos := listPosition{since: at.since, page: page}
		got, err := p.src.PollIssues(ctx, p.repo, ListIssuesOptions{Since: pos.since, Labels: p.labels, MaxPages: 1, Page: page}, "")
		if err != nil {
			return nil, err
		}
		read := entries(got)
		if page == at.page-1 && w.knows(at) && maps.Equal(read, w.last) {
			return nil, nil
		}
		out = append(out, backfill{at: pos, got: got})
		if page == 1 || overlaps(read, seen) {
			break
		}
	}
	return out, nil
}

// overlaps reports whether a and b share an entry.
func overlaps(a, b map[entry]bool) bool {
	for e := range a {
		if b[e] {
			return true
		}
	}
	return false
}

// listPosition is a place in the listing of updates: a page of the
// listing from since.
type listPosition struct {
	since time.Time
	page  int
}

// after is where the listing continues once the request at pos returned got:
// from the newest update read when that is in a later second, and otherwise
// on the next page of the same second, or on the same page when the
// listing ended there.
func (pos listPosition) after(got IssuePage) listPosition {
	switch {
	case got.Through.Truncate(time.Second).After(pos.since.Truncate(time.Second)):
		return listPosition{since: got.Through, page: 1}
	case got.More:
		return listPosition{since: pos.since, page: pos.page + 1}
	case got.Through.After(pos.since):
		return listPosition{since: got.Through, page: pos.page}
	}
	return pos
}
