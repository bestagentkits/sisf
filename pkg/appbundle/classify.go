package appbundle

import (
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/pkg/managedapp"
)

// Change is one difference between the Owner-approved baseline lockfile and
// a candidate's. From is empty for an added entry, To for a removed one.
type Change struct {
	Kind  string
	Slug  string
	From  string
	To    string
	Class managedapp.LockChange
}

// Classify lists every difference between baseline and candidate, in
// canonical order, each with its class. It runs on the factory side against
// the Owner-approved baseline; the candidate never chooses the baseline.
// An unchanged entry yields nothing. When in doubt the class is the stricter
// one: any change to a core entry is LockCore, a version that is not
// numeric X.Y.Z is LockMajor, and a changed sha256 under a version that
// compares equal is LockMajor because it is suspicious.
func Classify(baseline, candidate Lock) []Change {
	base := map[string]Entry{}
	for _, e := range baseline.Entries {
		base[e.key()] = e
	}
	cand := map[string]Entry{}
	for _, e := range candidate.Entries {
		cand[e.key()] = e
	}
	all := append([]Entry(nil), baseline.Entries...)
	for _, e := range candidate.Entries {
		if _, ok := base[e.key()]; !ok {
			all = append(all, e)
		}
	}
	sortEntries(all)

	var changes []Change
	for _, e := range all {
		b, inBase := base[e.key()]
		c, inCand := cand[e.key()]
		ch := Change{Kind: e.Kind, Slug: e.Slug, From: b.Version, To: c.Version}
		switch {
		case inBase && inCand && b == c:
			continue
		case e.rule().Core:
			ch.Class = managedapp.LockCore
		case !inBase:
			ch.Class = managedapp.LockAdded
		case !inCand:
			ch.Class = managedapp.LockRemoved
		default:
			ch.Class = versionClass(b, c)
		}
		changes = append(changes, ch)
	}
	return changes
}

// versionClass classifies an entry present on both sides that changed.
func versionClass(b, c Entry) managedapp.LockChange {
	bv, okB := parseVersion(b.Version)
	cv, okC := parseVersion(c.Version)
	if !okB || !okC {
		return managedapp.LockMajor
	}
	switch {
	case bv[0] != cv[0]:
		return managedapp.LockMajor
	case bv[1] != cv[1]:
		return managedapp.LockMinor
	case bv[2] != cv[2]:
		return managedapp.LockPatch
	}
	// Equal numbers but a different sha256 or spelling ("1.0" for "1.0.0"):
	// the same release should not change bytes.
	return managedapp.LockMajor
}

// parseVersion reads X, X.Y or X.Y.Z of decimal numbers; missing parts are
// 0. A pre-release suffix or any other form is not a version it can order.
func parseVersion(v string) ([3]uint64, bool) {
	var out [3]uint64
	parts := strings.Split(v, ".")
	if len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return out, false
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
