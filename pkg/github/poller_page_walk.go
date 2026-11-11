package github

import "time"

// pageWalk keeps a walk through updates sharing one second from skipping
// an issue.
//
// GitHub's since has a precision of one second, so a poller steps through
// more updates within one second than a page holds by page number. When an
// issue on a page already read changes during that walk, it leaves the
// second and moves to the end of the listing, and every issue after it
// moves back one place: the first issue of the next page lands on a page
// already read and would never be listed again. The issue that moved shows
// up again later in the listing with a newer update, which is how the walk
// notices: it then starts that second over at its first page. The issues
// read again are reported once (Poller.lastSeen), or deduplicated by the
// caller's inbox. Each start-over follows an issue leaving the second, so
// the walk ends.
type pageWalk struct {
	// seconds are the seconds stepped through by page number.
	seconds map[time.Time]bool
	// members are the issues read at their own second, by a request asking
	// from that second, with the update they had then.
	members map[int]time.Time
	// rewind is the earliest walked second an issue left; zero when none
	// did. It is where the poller continues once a poll was acknowledged.
	rewind time.Time
}

func newPageWalk() pageWalk {
	return pageWalk{seconds: map[time.Time]bool{}, members: map[int]time.Time{}}
}

// stepped notes that the listing from cursor is read past its first page.
func (w *pageWalk) stepped(cursor time.Time) { w.seconds[cursor.Truncate(time.Second)] = true }

// read takes in an issue a request asking from cursor listed, and reports
// whether it left a second being walked by page, so the walk starts over.
func (w *pageWalk) read(cursor time.Time, issue Issue) bool {
	number, sec := issue.Ref.Number, cursor.Truncate(time.Second)
	left := false
	if at, ok := w.members[number]; ok && issue.UpdatedAt.After(at) {
		delete(w.members, number)
		if from := at.Truncate(time.Second); w.seconds[from] {
			left = true
			if w.rewind.IsZero() || from.Before(w.rewind) {
				w.rewind = from
			}
		}
	}
	if issue.UpdatedAt.Truncate(time.Second).Equal(sec) {
		w.members[number] = issue.UpdatedAt
	}
	return left
}

// forget drops what can no longer shift an unread issue once the position
// is since: issues of seconds before it that were never walked by page and,
// when the listing was read to its end, everything before it, since an
// issue that left a walked second was read again by then.
func (w *pageWalk) forget(since time.Time, complete bool) {
	floor := since.Truncate(time.Second)
	for number, at := range w.members {
		if sec := at.Truncate(time.Second); sec.Before(floor) && (complete || !w.seconds[sec]) {
			delete(w.members, number)
		}
	}
	if complete {
		for sec := range w.seconds {
			if sec.Before(floor) {
				delete(w.seconds, sec)
			}
		}
	}
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
