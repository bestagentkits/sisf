package appbundle

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// maxAdvisoryBytes bounds an advisory snapshot.
const maxAdvisoryBytes = 4 << 20

// Advisory says that every version of one pinned archive below Fixed is
// vulnerable.
type Advisory struct {
	Kind, Slug string
	// Fixed is the first version that is not affected.
	Fixed string
	// ID names the advisory, such as a CVE.
	ID       string
	Severity string
}

// Finding is an entry of a lockfile an advisory applies to.
type Finding struct {
	Advisory
	Version string `json:"version"`
	// Unordered is true when the pinned version cannot be compared with
	// Fixed; it is reported as affected, since a missing fact is never safe.
	Unordered bool `json:"unordered,omitempty"`
}

var (
	advisoryID       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	advisorySeverity = map[string]bool{"low": true, "medium": true, "high": true, "critical": true}
)

// ParseAdvisories reads a pinned advisory snapshot: blank lines, comments
// starting with #, and lines "kind slug fixed-version id severity".
func ParseAdvisories(r io.Reader) ([]Advisory, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxAdvisoryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read advisories: %w", err)
	}
	if len(data) > maxAdvisoryBytes {
		return nil, fmt.Errorf("advisory snapshot is larger than %d bytes", maxAdvisoryBytes)
	}
	var out []Advisory
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 5 {
			return nil, fmt.Errorf("advisory line %d: want kind slug fixed-version id severity", n)
		}
		a := Advisory{Kind: f[0], Slug: f[1], Fixed: f[2], ID: f[3], Severity: f[4]}
		if kindIndex(a.Kind) < 0 || !slugPattern.MatchString(a.Slug) {
			return nil, fmt.Errorf("advisory line %d: unknown kind or bad slug", n)
		}
		if _, ok := parseVersion(a.Fixed); !ok {
			return nil, fmt.Errorf("advisory line %d: %q is not a version", n, a.Fixed)
		}
		if !advisoryID.MatchString(a.ID) || !advisorySeverity[a.Severity] {
			return nil, fmt.Errorf("advisory line %d: bad id or severity", n)
		}
		out = append(out, a)
	}
	return out, sc.Err()
}

// Scan returns the advisories that apply to lock, in lock order.
func Scan(lock Lock, advisories []Advisory) []Finding {
	var out []Finding
	for _, e := range lock.Entries {
		for _, a := range advisories {
			if a.Kind != e.Kind || a.Slug != e.Slug {
				continue
			}
			v, ok := parseVersion(e.Version)
			fixed, _ := parseVersion(a.Fixed)
			if !ok {
				out = append(out, Finding{Advisory: a, Version: e.Version, Unordered: true})
				continue
			}
			if less(v, fixed) {
				out = append(out, Finding{Advisory: a, Version: e.Version})
			}
		}
	}
	return out
}

func less(a, b [3]uint64) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
