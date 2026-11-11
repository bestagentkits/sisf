package release

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// UnitInputConflictKind says why a declared unit input pattern is refused.
type UnitInputConflictKind string

const (
	// ConflictProtected: the pattern can match a protected path. The
	// manifest cannot widen what a candidate may change into the trusted
	// control surface.
	ConflictProtected UnitInputConflictKind = "protected"
	// ConflictControlGoCode: a pattern of the control unit can match a file
	// the Go toolchain reads, which the installer and the simulation link
	// as well as the control unit.
	ConflictControlGoCode UnitInputConflictKind = "control_go_code"
	// ConflictOverlap: the pattern and an input of another unit can match
	// the same path, so one release would vouch for another unit's input.
	ConflictOverlap UnitInputConflictKind = "overlap"
)

// UnitInputConflict is one declared pattern that would be refused.
type UnitInputConflict struct {
	Kind    UnitInputConflictKind
	Unit    string
	Index   int
	Pattern string
	// Reason says what the pattern can match, in words fit for the Owner.
	Reason string
}

// UnitInputConflicts returns what is wrong with the unit input patterns a
// manifest declares, judged against the protected paths, the Go code of the
// control unit and the inputs of every other unit, declared or built in.
// It reports what it can prove with a path both sides match; what it cannot
// prove is still refused at release time, where a path two units consume,
// or a protected path, needs the Owner's approval.
func UnitInputConflicts(declared map[string][]string, protected []string) []UnitInputConflict {
	inputs, err := NewUnitInputs(declared)
	if err != nil {
		// Malformed patterns are the manifest validation's to report.
		return nil
	}
	var found []UnitInputConflict
	for _, unit := range slices.Sorted(maps.Keys(inputs.declared)) {
		for i, p := range inputs.declared[unit] {
			at := UnitInputConflict{Unit: unit, Index: i, Pattern: p}
			if entry, ok := protectedMatch(p, protected); ok {
				at.Kind, at.Reason = ConflictProtected, fmt.Sprintf("can match %s, which is protected; only the Owner changes it", entry)
				found = append(found, at)
			}
			if unit == controlUnit {
				if w, ok := goCodeMatch(p); ok {
					at.Kind, at.Reason = ConflictControlGoCode, fmt.Sprintf("can match %s, a file the Go toolchain reads; "+
						"the installer and the simulation link the control unit's Go code, so one observed release of control cannot vouch for it", w)
					found = append(found, at)
				}
			}
			for _, other := range inputs.units() {
				if other == unit {
					continue
				}
				if w, ok := inputs.overlap(p, other); ok {
					at.Kind, at.Reason = ConflictOverlap, fmt.Sprintf("can match %s, which the unit %s consumes as well; a path is the input of one unit", w, other)
					found = append(found, at)
				}
			}
		}
	}
	return found
}

// protectedMatch returns the protected entry the pattern can match a path
// of: a protected file it matches, or a protected directory whose segments
// its leading segments match and below which it reaches.
func protectedMatch(pattern string, protected []string) (string, bool) {
	segments := strings.Split(strings.ToLower(pattern), "/")
	for _, entry := range protected {
		lower := strings.ToLower(entry)
		dir, isDir := strings.CutSuffix(lower, "/")
		if !isDir {
			if manifest.MatchUnitInput(pattern, lower) {
				return entry, true
			}
			continue
		}
		parts := strings.Split(dir, "/")
		if len(segments) <= len(parts) {
			continue
		}
		if slices.EqualFunc(segments[:len(parts)], parts, segmentMatches) {
			return entry, true
		}
	}
	return "", false
}

// segmentMatches reports whether the pattern segment matches the name.
func segmentMatches(pattern, name string) bool {
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}

// goCodeMatch returns a file the Go toolchain reads that the pattern
// matches, if it finds one: each path the pattern spells, with its
// extension or its name swapped for one the Go toolchain reads, so a
// wildcard or a character class in the extension is found as well.
func goCodeMatch(pattern string) (string, bool) {
	fills := []string{"x"}
	for _, ext := range goToolchainExtensions {
		fills = append(fills, "x"+ext)
	}
	fills = append(fills, goToolchainFiles...)
	var candidates []string
	for _, w := range witnesses(pattern, fills) {
		candidates = append(candidates, w)
		for _, ext := range goToolchainExtensions {
			candidates = append(candidates, strings.TrimSuffix(w, path.Ext(w))+ext)
		}
		for _, name := range goToolchainFiles {
			candidates = append(candidates, path.Join(path.Dir(w), name))
		}
	}
	for _, w := range candidates {
		if goToolchainFile(w) && manifest.MatchUnitInput(pattern, w) {
			return w, true
		}
	}
	return "", false
}

// overlap returns a path the pattern matches that other consumes, if it
// finds one: a path of the pattern itself, one of the other unit's own
// patterns or built-in directories laid over it, or one both patterns of
// the same depth spell.
func (u UnitInputs) overlap(pattern, other string) (string, bool) {
	consumed := func(w string) bool { return manifest.MatchUnitInput(pattern, w) && u.consumes(other, w) }
	fills := []string{"x", "", "x.html", "x.txt"}
	candidates := witnesses(pattern, fills)
	otherPatterns, declared := u.declared[other]
	switch {
	case declared:
		for _, q := range otherPatterns {
			candidates = append(candidates, witnesses(q, fills)...)
			if w, ok := jointWitness(pattern, q); ok {
				candidates = append(candidates, w)
			}
		}
	case other == appUnit:
		for _, d := range appUnitPackageDirs {
			candidates = append(candidates, overlay(pattern, strings.ToLower(d), fills)...)
		}
		candidates = append(candidates, appUnitFiles...)
	case other == controlUnit:
		candidates = append(candidates, overlay(pattern, controlUnitDir, fills)...)
	}
	for _, w := range candidates {
		if consumed(w) {
			return w, true
		}
	}
	return "", false
}

// witnesses spells paths the pattern may match, filling every wildcard of a
// segment with each fill in turn; only those it does match are returned.
func witnesses(pattern string, fills []string) []string {
	segments := strings.Split(strings.ToLower(pattern), "/")
	var out []string
	for _, fill := range fills {
		spelled := make([]string, len(segments))
		for i, seg := range segments {
			spelled[i] = spell(seg, fill)
		}
		// The last segment takes the fill; the ones before it a plain name.
		for i := range segments[:len(segments)-1] {
			spelled[i] = spell(segments[i], "x")
		}
		if w := strings.Join(spelled, "/"); manifest.MatchUnitInput(pattern, w) {
			out = append(out, w)
		}
	}
	return out
}

// overlay spells paths the pattern may match below dir: dir's segments
// where the pattern's leading segments match them, the rest filled.
func overlay(pattern, dir string, fills []string) []string {
	segments := strings.Split(strings.ToLower(pattern), "/")
	parts := strings.Split(strings.TrimSuffix(dir, "/"), "/")
	if len(segments) <= len(parts) || !slices.EqualFunc(segments[:len(parts)], parts, segmentMatches) {
		return nil
	}
	var out []string
	for _, w := range witnesses(strings.Join(segments[len(parts):], "/"), fills) {
		out = append(out, strings.Join(parts, "/")+"/"+w)
	}
	return out
}

// jointWitness spells one path both patterns of the same depth match, when
// each pair of segments has a name built from their spellings that both
// match.
func jointWitness(p, q string) (string, bool) {
	ps, qs := strings.Split(strings.ToLower(p), "/"), strings.Split(strings.ToLower(q), "/")
	if len(ps) != len(qs) {
		return "", false
	}
	names := make([]string, len(ps))
	for i := range ps {
		found := false
		for _, fill := range []string{"", "x"} {
			a, b := spell(ps[i], fill), spell(qs[i], fill)
			for _, name := range []string{a, b, a + b, b + a} {
				if name != "" && segmentMatches(ps[i], name) && segmentMatches(qs[i], name) {
					names[i], found = name, true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return "", false
		}
	}
	return strings.Join(names, "/"), true
}

// spell writes a name a pattern segment may match: each * becomes fill,
// each ? an x and each character class the first character it lists.
func spell(segment, fill string) string {
	var b strings.Builder
	for i := 0; i < len(segment); i++ {
		switch c := segment[i]; c {
		case '*':
			b.WriteString(fill)
		case '?':
			b.WriteByte('x')
		case '[':
			end := strings.IndexByte(segment[i+1:], ']')
			if end < 0 {
				b.WriteByte(c)
				continue
			}
			class := strings.TrimPrefix(segment[i+1:i+1+end], "^")
			if strings.HasPrefix(segment[i+1:], "^") || class == "" {
				b.WriteByte('~')
			} else {
				b.WriteByte(class[0])
			}
			i += end + 1
		case '\\':
			if i+1 < len(segment) {
				i++
				b.WriteByte(segment[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
