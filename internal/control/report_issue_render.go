package control

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/bestagentkits/sisf/internal/intake/report"
)

// Bounds of the issue a report is published as. GitHub takes a body of up to
// 65,536 characters; a report's own text is far smaller than these.
const (
	reportIssueTitlePrefix  = "Report: "
	maxReportIssueTitleRune = 120
	maxReportIssueBodyRunes = 8000
)

// zeroWidthSpace is put inside text that GitHub would otherwise read as a
// mention, a reference or a comment, so it shows the same and means nothing.
const zeroWidthSpace = "\u200b"

var (
	// mentionForm is an @ that GitHub reads as a mention of an account or
	// a team.
	mentionForm = regexp.MustCompile(`@([A-Za-z0-9])`)
	// referenceForm is a # that GitHub reads as a reference to an issue or
	// a pull request, alone (#12) or after a repository (owner/repo#12).
	referenceForm = regexp.MustCompile(`#([0-9])`)
	// shortReferenceForm is GitHub's other short reference, GH-12.
	shortReferenceForm = regexp.MustCompile(`(?i)\b(gh)(-[0-9])`)
	// tildeRun finds the runs of ~ that could close a fence.
	tildeRun = regexp.MustCompile(`~+`)
)

// defuse makes text from a report inert on GitHub: no mention notifies
// anyone, no reference links or cross-references an issue, and no HTML
// comment, and so no marker the factory reads in issues and comments, can
// be formed. Control and format characters, which can hide or reorder
// text, are dropped; newlines and tabs are kept only when keepLines is set.
func defuse(text string, keepLines bool) string {
	text = strings.Map(func(r rune) rune {
		switch {
		case keepLines && (r == '\n' || r == '\t'):
			return r
		case r == '\n' || r == '\t' || r == '\r':
			return ' '
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, text)
	text = strings.ReplaceAll(text, "<!--", "<!"+zeroWidthSpace+"--")
	text = mentionForm.ReplaceAllString(text, "@"+zeroWidthSpace+"$1")
	text = referenceForm.ReplaceAllString(text, "#"+zeroWidthSpace+"$1")
	return shortReferenceForm.ReplaceAllString(text, "$1"+zeroWidthSpace+"$2")
}

// cut bounds text to limit runes, saying so when it cut.
func cut(text string, limit int, mark string) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + mark
}

// fenced puts text in a fenced block that no run of ~ in it can close.
func fenced(text string) string {
	longest := 2
	for _, run := range tildeRun.FindAllString(text, -1) {
		longest = max(longest, len(run))
	}
	fence := strings.Repeat("~", longest+1)
	return fence + "text\n" + text + "\n" + fence
}

// reportIssueTitle is the title of the issue for r: a fixed prefix and the
// report's title on one line, defused and bounded.
func reportIssueTitle(r report.Report) string {
	title := strings.Join(strings.Fields(defuse(r.Title, false)), " ")
	if title == "" {
		title = "(no title)"
	}
	return reportIssueTitlePrefix + cut(title, maxReportIssueTitleRune, "…")
}

// reportIssueBody is the body of the issue for r. It opens with the marker
// that names the report, says under a fixed header where the text came from
// and that nothing checked it, and gives the text defused in fenced blocks,
// with only when it was received and how often it was sent: nothing about
// who sent it.
func reportIssueBody(r report.Report) string {
	title := defuse(r.Title, false)
	body := cut(defuse(r.Body, true), maxReportIssueBodyRunes, "\n[cut to "+fmt.Sprint(maxReportIssueBodyRunes)+" characters]")
	var b strings.Builder
	b.WriteString(report.IssueMarker(r.ID) + "\n")
	b.WriteString("### Unverified report from the public report endpoint\n\n")
	b.WriteString("Someone outside the project sent this through the factory's public report endpoint. " +
		"Nobody has checked it, it may be wrong or hostile, and the factory starts no work from it. " +
		"A person decides what to do with it.\n\n")
	fmt.Fprintf(&b, "- Received: %s\n- Report: `%s`\n- Times sent: %d\n\n", r.ReceivedAt.UTC().Format(time.RFC3339), r.ID, max(r.Count, 1))
	b.WriteString("**Title as sent**\n\n" + fenced(title) + "\n\n")
	b.WriteString("**Report as sent**\n\n" + fenced(body) + "\n")
	return b.String()
}
