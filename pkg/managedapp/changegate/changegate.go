// Package changegate decides, on the supervisor's side, whether a candidate
// change to a managed application may be released by the factory alone or
// needs a signed Owner approval.
//
// It judges against the Owner-approved baseline: the Spec and the lockfile
// come from the approved branch, never from the candidate, and the
// candidate's lockfile is only compared with them. A sha256 in a lockfile
// the candidate edited proves the archive's integrity, not that the Owner
// chose it: that is why every class of lock change is decided here by the
// baseline's policy, and the Spec, the lockfile path and the protected
// patterns cannot be changed by the candidate at all.
package changegate

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/bestagentkits/sisf/pkg/appbundle"
	"github.com/bestagentkits/sisf/pkg/managedapp"
)

// Input is what one candidate change is judged on.
type Input struct {
	// Baseline is the Spec on the Owner-approved branch.
	Baseline *managedapp.Spec
	// BaselineLock and CandidateLock are the lockfiles of the approved
	// branch and the candidate; both empty for an application without one.
	BaselineLock, CandidateLock *appbundle.Lock
	// Changed are the repository paths the candidate changes, relative to
	// the application's directory, slash-separated.
	Changed []string
}

// Verdict says who may release the change and why.
type Verdict struct {
	// Owner is true when only a signed Owner approval releases the change.
	Owner bool `json:"owner"`
	// Reasons say, in fixed words, what made the change the Owner's; empty
	// when the factory may release it alone.
	Reasons []string `json:"reasons,omitempty"`
	// Lock lists the lockfile changes and their classes.
	Lock []appbundle.Change `json:"lock,omitempty"`
}

// Judge decides who may release in. A change it cannot read is the Owner's.
func Judge(in Input) Verdict {
	var v Verdict
	owner := func(format string, args ...any) {
		v.Owner = true
		v.Reasons = append(v.Reasons, fmt.Sprintf(format, args...))
	}
	if in.Baseline == nil {
		owner("there is no approved Spec to judge the change against")
		return v
	}
	lockPath := in.Baseline.Bundle.Lock
	for _, p := range in.Changed {
		clean := path.Clean(p)
		switch {
		case !isLocal(clean):
			owner("path %q is outside the application", p)
		case clean == managedapp.FileName:
			owner("the change edits %s", managedapp.FileName)
		case lockPath != "" && clean == lockPath:
			// Judged by its content below.
		default:
			if pat, ok := protected(in.Baseline.Policy.Protected, clean); ok {
				owner("path %s is protected by %q", clean, pat)
			}
		}
	}
	if lockPath != "" && changes(in.Changed, lockPath) {
		if in.BaselineLock == nil || in.CandidateLock == nil {
			owner("the lockfile change cannot be read")
		} else {
			v.Lock = appbundle.Classify(*in.BaselineLock, *in.CandidateLock)
			for _, c := range v.Lock {
				if in.Baseline.Policy.Decide(c.Class) == managedapp.DecisionOwner {
					owner("%s %s %s -> %s is a %s change", c.Kind, c.Slug, orNone(c.From), orNone(c.To), c.Class)
				}
			}
		}
	}
	sort.Strings(v.Reasons)
	return v
}

// protected returns the first pattern that covers p or a directory above it,
// so "plugins/pay/*" protects everything under plugins/pay.
func protected(patterns []string, p string) (string, bool) {
	for _, pat := range patterns {
		for q := p; q != "." && q != "/"; q = path.Dir(q) {
			if ok, err := path.Match(strings.ToLower(pat), strings.ToLower(q)); err == nil && ok {
				return pat, true
			}
		}
	}
	return "", false
}

func changes(changed []string, p string) bool {
	for _, c := range changed {
		if path.Clean(c) == p {
			return true
		}
	}
	return false
}

func isLocal(p string) bool {
	return p != "" && p != "." && !path.IsAbs(p) && p != ".." && !strings.HasPrefix(p, "../")
}

func orNone(v string) string {
	if v == "" {
		return "none"
	}
	return v
}
