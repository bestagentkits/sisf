package intake

import (
	"regexp"
	"strings"
)

// sections is the text of an issue body or a reply, split by its Markdown
// headings into the parts intake knows.
type sections struct {
	// Preamble is the text before the first heading.
	Preamble string
	// Scope is the text under headings that name the scope.
	Scope string
	// Other is the text under every other heading, headings included.
	Other string
	// DoD and Features are the texts under their headings.
	DoD      string
	Features string
}

// The headings intake recognizes, in lower case without punctuation.
var (
	scopeHeadings   = []string{"scope", "scope of work", "description", "summary", "problem", "what should change"}
	dodHeadings     = []string{"definition of done", "dod", "acceptance criteria", "done when"}
	featureHeadings = []string{"affected features", "affected feature", "features", "feature"}
)

var (
	headingPattern = regexp.MustCompile(`^ {0,3}#{1,6}\s+(.*?)\s*#*\s*$`)
	fencePattern   = regexp.MustCompile("^ {0,3}(```|~~~)")
	listPattern    = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+(?:\[[ xX]\]\s+)?(.*)$`)
)

// noResponse is what a GitHub issue form writes into a field left empty.
const noResponse = "_No response_"

// parseSections splits text at its headings. A heading inside a fenced code
// block is code, not a heading.
func parseSections(text string) sections {
	var out sections
	target := &out.Preamble
	fenced := false
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if fencePattern.MatchString(line) {
			fenced = !fenced
		}
		if match := headingPattern.FindStringSubmatch(line); match != nil && !fenced {
			switch name := headingName(match[1]); {
			case contains(scopeHeadings, name):
				target = &out.Scope
			case contains(dodHeadings, name):
				target = &out.DoD
			case contains(featureHeadings, name):
				target = &out.Features
			default:
				target = &out.Other
				*target += line + "\n"
			}
			continue
		}
		if strings.TrimSpace(line) == noResponse {
			continue
		}
		*target += line + "\n"
	}
	return out
}

// headingName reduces a heading to the form the lists above use.
func headingName(heading string) string {
	name := strings.ToLower(strings.Trim(strings.TrimSpace(heading), "*_`:"))
	return strings.Join(strings.Fields(strings.TrimRight(name, ":")), " ")
}

func contains(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

// listItems returns the entries of a section: its list items, or each
// non-empty line when it has no list. Checkbox marks are dropped and inner
// whitespace is collapsed, so reformatting a list is not an edit.
func listItems(text string) []string {
	var listed, lines []string
	for _, line := range strings.Split(text, "\n") {
		if match := listPattern.FindStringSubmatch(line); match != nil {
			if item := collapse(match[1]); item != "" {
				listed = append(listed, item)
			}
			continue
		}
		if item := collapse(line); item != "" {
			lines = append(lines, item)
		}
	}
	if len(listed) > 0 {
		return listed
	}
	return lines
}

// collapse trims text and reduces every run of whitespace to one space.
func collapse(text string) string { return strings.Join(strings.Fields(text), " ") }

// normalizeText puts free text into the form that is compared and hashed:
// one kind of line ending, no trailing spaces, no runs of blank lines.
func normalizeText(text string) string {
	var lines []string
	blank := true
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			if !blank {
				lines = append(lines, "")
			}
			blank = true
			continue
		}
		blank = false
		lines = append(lines, line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
