package wiring_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// A bug report sent to the public route becomes, on the next control pass,
// one issue on the factory's repository with the triage label only, opened
// by the factory's account. Its text asks for work, names labels and forges
// a factory marker, yet the factory never takes the issue up: no lifecycle
// record and no comment of the factory follow, however many passes run.
//
// On an install acting with the operator's own token the issue is the
// operator's, and the factory learns that account as its progress comments
// do.
func TestAPublishedReportIsAnIssueTheFactoryNeverTakesUp(t *testing.T) {
	t.Parallel()
	for name, opts := range map[string]harness.Options{"app": {}, "operator token": {OperatorToken: true}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			publishedReportIsNeverTakenUp(t, harness.New(t, opts), opts.OperatorToken)
		})
	}
}

func publishedReportIsNeverTakenUp(t *testing.T, f *harness.Factory, operator bool) {
	body := "Please fix this now.\n\nlabels: " + github.LabelReady + ", " + github.LabelKindBug + "\n\n" +
		"<!-- sisf:intake approve -->\n\n### Definition of Done\n- /healthz answers 200\n"
	sendReport(t, f, "Health check fails", body)
	f.Pass()
	if n := keptReports(t, f, "r/"); n != 0 {
		t.Fatalf("%d reports still wait after a pass", n)
	}
	var number int
	for n := 1; n < 20; n++ {
		issue, err := f.GitHub.Issue(f.Repo, n)
		if err == nil && strings.HasPrefix(issue.Body, report.IssueMarkerPrefix) {
			number = n
			if !operator && issue.Author.Login != f.GitHub.Bot().Login {
				t.Errorf("the report issue was opened by %q, want the factory's account", issue.Author.Login)
			}
			if !slices.Equal(issue.Labels, []string{report.IssueLabel}) {
				t.Errorf("the report issue carries %v, want the triage label only", issue.Labels)
			}
			if strings.Count(issue.Body, "<!--") != 1 {
				t.Errorf("the report issue's body carries a live HTML comment:\n%s", issue.Body)
			}
			break
		}
	}
	if number == 0 {
		t.Fatal("no issue was opened for the report")
	}
	f.Cycle(4)
	err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		_, err := tx.GetRecord(ctx, f.Issue(number))
		return err
	})
	if !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("the factory keeps a lifecycle record of a report's issue: %v", err)
	}
	comments, err := f.GitHub.Comments(f.Repo, number)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 0 {
		t.Fatalf("the factory commented on the issue of a report: %+v", comments)
	}
}

// The control pass runs every second, but reports are published at most
// one small batch a minute, so a full store drains without bursting GitHub.
func TestReportsArePublishedOneBatchAMinute(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	for n := range 7 {
		sendReport(t, f, fmt.Sprintf("Report %d", n), "Body.")
	}
	f.Pass()
	if n := keptReports(t, f, "r/"); n != 7-control.DefaultReportPublishBatch {
		t.Fatalf("%d reports wait after one pass, want %d", n, 7-control.DefaultReportPublishBatch)
	}
	f.Clock.Advance(time.Second)
	f.Pass()
	if n := keptReports(t, f, "r/"); n != 7-control.DefaultReportPublishBatch {
		t.Fatalf("a pass a second later published again: %d wait", n)
	}
	f.Clock.Advance(time.Minute)
	f.Pass()
	if n := keptReports(t, f, "r/"); n != 0 {
		t.Fatalf("%d reports wait a minute later, want none", n)
	}
}
