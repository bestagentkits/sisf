package wiring

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/internal/redact"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// refusingIssues fails the test on any call: nothing may reach GitHub.
type refusingIssues struct{ t *testing.T }

func (r refusingIssues) CreateIssue(context.Context, github.RepoRef, github.NewIssue) (github.Issue, error) {
	r.t.Fatal("an issue was opened for a report on an install that names the factory's account by ID only")
	return github.Issue{}, nil
}

func (r refusingIssues) PollIssues(context.Context, github.RepoRef, github.ListIssuesOptions, string) (github.IssuePage, error) {
	r.t.Fatal("report issues were listed on an install that names the factory's account by ID only")
	return github.IssuePage{}, nil
}

// An install that names the factory's account by its ID only cannot tell
// an issue the factory opened from one someone else did, so it publishes
// no report and says why once, not on every pass.
func TestAnInstallNamingTheFactoryByIDOnlyPublishesNoReport(t *testing.T) {
	t.Parallel()
	var logged bytes.Buffer
	c := &Control{cfg: ControlConfig{Clock: core.SystemClock{}}, log: slog.New(slog.NewTextHandler(&logged, nil)), backoff: map[string]retryAt{},
		reports: &control.ReportPublisher{Issues: refusingIssues{t}, Self: github.Identity{ID: 7}, Redactor: redact.New(redact.Config{})}}
	for range 3 {
		if err := c.publishReports(context.Background()); err != nil {
			t.Fatalf("a pass on an install naming the factory by ID only: %v", err)
		}
	}
	if n := strings.Count(logged.String(), "not published"); n != 1 {
		t.Fatalf("the install said %d times why it publishes no report, want once:\n%s", n, logged.String())
	}
}
