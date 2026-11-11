package report

import (
	"strings"

	"github.com/bestagentkits/sisf/pkg/github"
)

// IssueLabel is the one label of the GitHub issue the control zone opens
// for a stored report, so a person can find the reports to triage. It
// asks for nothing: it is not sisf:ready, and as a sisf: label no ready
// alias can name it, so intake never takes such an issue up by itself.
const IssueLabel = "sisf:report"

// IssueMarkerPrefix opens the first line of the body of an issue opened for
// a report; the report's ID and " -->" follow. A report's own text cannot
// carry it: every "<!--" in that text is defused before it is published.
const IssueMarkerPrefix = "<!-- sisf:report "

// IssueMarker is the first line of the body of the issue opened for the
// report with id.
func IssueMarker(id string) string { return IssueMarkerPrefix + id + " -->" }

// IsReportIssue reports whether issue was opened for a public report: it
// carries IssueLabel, or its body starts with the marker. Its text came
// from someone outside the project, whoever the issue's author is on
// GitHub, so a person asking for work on it does not make that text the
// request of anyone with access to the repository.
func IsReportIssue(issue github.Issue) bool {
	return issue.HasLabel(IssueLabel) || strings.HasPrefix(issue.Body, IssueMarkerPrefix)
}
