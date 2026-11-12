package catalog

import (
	"fmt"
	"strings"
)

// changelogHeader starts a changelog the repository does not have yet.
const changelogHeader = "# CHANGELOG.md\n\nReleases the factory verified, newest first. Each entry names the issue and the release digest.\n"

// releaseMarker is the hidden marker that makes one release's entry unique.
func releaseMarker(e Entry) string { return "<!-- sisf:release " + string(e.RunID) + " -->" }

// AddChangelogEntry puts the release's entry at the top of the changelog,
// below its introduction. An entry for the same run already present leaves
// the changelog as it is and returns false, so a release replayed after a
// crash is recorded once.
func AddChangelogEntry(content []byte, e Entry) ([]byte, bool) {
	text := string(content)
	marker := releaseMarker(e)
	if strings.Contains(text, marker) {
		return content, false
	}
	if strings.TrimSpace(text) == "" {
		text = changelogHeader
	}
	summary := plain(e.Summary)
	if summary == "" {
		summary = "Release of " + plain(e.Unit)
	}
	line := fmt.Sprintf("- %s: %s (%s, %s %s) %s\n",
		e.VerifiedAt.UTC().Format("2006-01-02"), summary, e.IssueID.String(), plain(e.Unit), e.Release, marker)
	// The entry goes before the first existing entry, or at the end of the
	// introduction when there is none.
	if at := strings.Index(text, "\n- "); at >= 0 {
		return []byte(text[:at+1] + line + text[at+1:]), true
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return []byte(text + "\n" + line), true
}
