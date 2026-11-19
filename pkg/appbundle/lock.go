package appbundle

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// maxLockBytes bounds a lockfile; a real one is a few hundred bytes.
const maxLockBytes = 1 << 20

var (
	slugPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.-]{0,63}$`)
	shaPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// kindRule is everything particular to one kind of lockfile entry. Adding a
// kind, for this stack or another, is a row in kinds; no code branches on a
// kind's name. In the templates, {slug} and {version} are replaced.
type kindRule struct {
	Name string
	// URL is where the archive is downloaded from.
	URL string
	// Strip is the directory every archive entry must sit under; it is
	// removed from the entry's name. An entry outside it is refused.
	Strip string
	// Into is the bundle directory the stripped entries are unpacked into.
	// Whatever an earlier archive put there is replaced, so a pinned plugin
	// or theme supersedes the copy the core ships.
	Into string
	// Core marks the foundation of the bundle: at most one per lockfile,
	// unpacked first, and any change to it is a managedapp.LockCore change.
	Core bool
}

// kinds is ordered: archives are unpacked in this order, then by slug.
var kinds = []kindRule{
	{Name: "core", URL: "https://downloads.wordpress.org/release/wordpress-{version}.zip", Strip: "wordpress/", Core: true},
	{Name: "plugin", URL: "https://downloads.wordpress.org/plugin/{slug}.{version}.zip", Strip: "{slug}/", Into: "wp-content/plugins/{slug}/"},
	{Name: "theme", URL: "https://downloads.wordpress.org/theme/{slug}.{version}.zip", Strip: "{slug}/", Into: "wp-content/themes/{slug}/"},
}

func kindIndex(name string) int {
	for i, k := range kinds {
		if k.Name == name {
			return i
		}
	}
	return -1
}

// Entry is one pinned archive.
type Entry struct {
	Kind    string
	Slug    string
	Version string
	// SHA256 is the lowercase hex sha256 of the archive.
	SHA256 string
}

func (e Entry) rule() kindRule { return kinds[kindIndex(e.Kind)] }

func (e Entry) expand(template string) string {
	return strings.NewReplacer("{slug}", e.Slug, "{version}", e.Version).Replace(template)
}

// URL is where the entry's archive is downloaded from.
func (e Entry) URL() string { return e.expand(e.rule().URL) }

func (e Entry) key() string { return e.Kind + " " + e.Slug }

// Lock is a parsed lockfile. Entries are in canonical order (kind, then
// slug), whatever the order of the file's lines.
type Lock struct {
	Entries []Entry
}

// ParseLock reads a lockfile: blank lines, comment lines starting with #,
// and lines "kind slug version sha256". Anything else is an error.
func ParseLock(r io.Reader) (Lock, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxLockBytes+1))
	if err != nil {
		return Lock{}, fmt.Errorf("read lockfile: %w", err)
	}
	if len(data) > maxLockBytes {
		return Lock{}, fmt.Errorf("lockfile is larger than %d bytes", maxLockBytes)
	}
	var lock Lock
	seen := map[string]bool{}
	cores := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 4096), maxLockBytes)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		e, err := parseEntry(line)
		if err != nil {
			return Lock{}, fmt.Errorf("lockfile line %d: %w", n, err)
		}
		if seen[e.key()] {
			return Lock{}, fmt.Errorf("lockfile line %d: %s %s is pinned twice", n, e.Kind, e.Slug)
		}
		seen[e.key()] = true
		if e.rule().Core {
			if cores++; cores > 1 {
				return Lock{}, fmt.Errorf("lockfile line %d: more than one %s entry", n, e.Kind)
			}
		}
		lock.Entries = append(lock.Entries, e)
	}
	if err := sc.Err(); err != nil {
		return Lock{}, fmt.Errorf("read lockfile: %w", err)
	}
	sortEntries(lock.Entries)
	return lock, nil
}

func parseEntry(line string) (Entry, error) {
	f := strings.Fields(line)
	if len(f) != 4 {
		return Entry{}, fmt.Errorf("want \"kind slug version sha256\", got %d fields", len(f))
	}
	e := Entry{Kind: f[0], Slug: f[1], Version: f[2], SHA256: f[3]}
	switch {
	case kindIndex(e.Kind) < 0:
		return Entry{}, fmt.Errorf("unknown kind %q", e.Kind)
	case !slugPattern.MatchString(e.Slug):
		return Entry{}, fmt.Errorf("slug %q is not lowercase letters, digits and dashes", e.Slug)
	case !versionPattern.MatchString(e.Version):
		return Entry{}, fmt.Errorf("version %q is not letters, digits, dots and dashes", e.Version)
	case !shaPattern.MatchString(e.SHA256):
		return Entry{}, fmt.Errorf("sha256 %q is not 64 lowercase hex digits", e.SHA256)
	}
	return e, nil
}

func sortEntries(es []Entry) {
	sort.Slice(es, func(i, j int) bool {
		ki, kj := kindIndex(es[i].Kind), kindIndex(es[j].Kind)
		if ki != kj {
			return ki < kj
		}
		return es[i].Slug < es[j].Slug
	})
}
