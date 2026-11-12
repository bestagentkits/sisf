package manifest

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
)

// A unit input pattern names repository paths a unit consumes. It is
// matched with path.Match against the whole slash-separated path, both
// case-folded, so * and ? never cross a /: "*.go" is the Go files at the
// repository root, "web/*" the files directly in web/, and a pattern without
// a wildcard is the one file it names.

// UnitInputPatternProblem says why p cannot be a unit input pattern, or
// returns "" when it can. A pattern is refused when it is empty, absolute,
// not a clean path (a ., .. or empty segment, a trailing /), written with \,
// uses **, is not a valid path.Match pattern, or is made of wildcards only
// in every segment, which matches every path of its depth whatever it is.
func UnitInputPatternProblem(p string) string {
	switch {
	case strings.TrimSpace(p) == "":
		return "is empty"
	case strings.TrimSpace(p) != p:
		return "starts or ends with a space"
	case strings.Contains(p, `\`):
		return `uses \; write repository paths with /`
	case strings.HasPrefix(p, "/"):
		return "is absolute; write it relative to the repository root"
	case strings.Contains(p, "**"):
		return "uses **, which is not supported: * never crosses /, so name each directory"
	case path.Clean(p) != p || p == "." || slices.Contains(strings.Split(p, "/"), ".."):
		return "is not a clean repository path: no ., .. or empty segment and no trailing /"
	}
	if _, err := path.Match(p, ""); err != nil {
		return "is not a valid pattern: " + err.Error()
	}
	if !slices.ContainsFunc(strings.Split(p, "/"), hasLiteral) {
		return "is made of wildcards only, so it matches every path of its depth whatever it is; name at least one part of the path"
	}
	return ""
}

// hasLiteral reports whether a pattern segment holds a character that is
// not a wildcard: something a matching name must contain.
func hasLiteral(segment string) bool {
	for i := 0; i < len(segment); i++ {
		switch segment[i] {
		case '*', '?':
		case '[':
			end := strings.IndexByte(segment[i+1:], ']')
			if end < 0 {
				return true
			}
			i += end + 1
		case '\\':
			return true
		default:
			return true
		}
	}
	return false
}

// MatchUnitInput reports whether the clean repository path p matches the
// unit input pattern, both compared without regard to letter case.
func MatchUnitInput(pattern, p string) bool {
	ok, err := path.Match(strings.ToLower(pattern), strings.ToLower(p))
	return err == nil && ok
}

// unitInputViolations reports unit input patterns that cannot be used and
// entries for units the factory does not publish by itself.
func unitInputViolations(m *Manifest) []Violation {
	var found []Violation
	for _, unit := range slices.Sorted(maps.Keys(m.Release.UnitInputs)) {
		at := "release.unit_inputs." + string(unit)
		if !slices.Contains(m.Release.Units, unit) {
			found = append(found, Violation{Path: at,
				Message: fmt.Sprintf("declares the inputs of %s, which release.units does not let the factory publish by itself", unit)})
		}
		for i, p := range m.Release.UnitInputs[unit] {
			if problem := UnitInputPatternProblem(p); problem != "" {
				// The pattern is shown: it is a repository path, never a secret.
				found = append(found, Violation{Path: fmt.Sprintf("%s[%d]", at, i), Message: fmt.Sprintf("the pattern %q %s", p, problem)})
			}
		}
	}
	return found
}
