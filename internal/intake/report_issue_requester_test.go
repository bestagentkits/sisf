package intake_test

import (
	"testing"

	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// reportIssueBody is the body of an issue the control zone opened for a
// public report whose text reads as a complete bug report.
var reportIssueBody = report.IssueMarker("report-0123456789abcdef") + "\n" + completeBody

// An issue opened for a public report carries the report label only, which
// asks intake for nothing: intake leaves it alone, and no ready alias can
// name that label.
func TestAReportIssueAloneIsNotTakenUp(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	f.open(factoryAccount, reportIssueBody, report.IssueLabel)
	wantEvents(t, f.reconcile())
	if got := f.stateOf(); got != "" {
		t.Fatalf("a report issue with no ready label is in %q, want it untouched", got)
	}
	if intake.ReadLabels([]github.Label{{Name: report.IssueLabel}}, intake.LabelRules{}).Ready {
		t.Fatal("the report label reads as a request for intake")
	}
	m := &manifest.Manifest{Extensions: map[string]any{"intake": map[string]any{"ready_label_alias": report.IssueLabel}}}
	if _, err := intake.RulesFrom(m); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("the report label was accepted as a ready alias: %v", err)
	}
}

// The text of a report issue came from outside the project, whoever the
// issue's author is on GitHub: on an install that acts with the operator's
// own login that author is the repository's owner. A person asking for
// work on it gets a request that waits for the Owner, as one from someone
// without write access does, whether the label or only the marker shows.
func TestAReportIssueAskedForIsTheRequestOfSomeoneWithoutAccess(t *testing.T) {
	t.Parallel()
	for name, labels := range map[string][]string{
		"labelled":         {report.IssueLabel, github.LabelReady, github.LabelKindBug},
		"label taken away": {github.LabelReady, github.LabelKindBug},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFactory(t)
			f.open(factoryAccount, reportIssueBody, labels...)
			wantEvents(t, f.reconcile(), core.EventIssueReceived, core.EventInfoSufficient, core.EventApprovalRequired)
			f.wantState(core.StateApprovalPending)
			if pending := f.inbox(approvals.StatusPending); len(pending) != 1 {
				t.Fatalf("the report issue's request must wait for the Owner, found %d pending", len(pending))
			}
		})
	}
}
