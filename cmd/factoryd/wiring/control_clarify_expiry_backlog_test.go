package wiring_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/testkit/harness"
)

// openUnlabeled opens n issues without a label: each is one more inbox
// entry for intake, which it finishes without work.
func openUnlabeled(f *harness.Factory, n int) {
	for i := range n {
		f.OpenIssue("someone", "NONE", fmt.Sprintf("Question %d", i), "Just asking.")
	}
}

// An answer polled behind more entries than one drain of the inbox hands to
// intake is taken in before the issue is parked: the issue waits while the
// inbox still holds entries.
func TestAnAnswerBehindAFullInboxBatchIsTakenInBeforeTheIssueIsParked(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{OperatorToken: true})
	number := askedIssue(t, f)
	f.Clock.Advance(clarifyTimeout - 30*time.Second)
	f.Pass()
	openUnlabeled(f, 120)
	f.Comment(number, "maintainer", "MEMBER", answer)
	f.Clock.Advance(30 * time.Second)
	for range 3 {
		f.Pass()
	}
	wantAnswered(t, f, number)
}

// An answer whose handling failed is held back for another try, and the
// issue is not parked meanwhile: neither in the pass whose drain failed nor
// in a later one while the answer waits for its next try.
func TestAnAnswerWhoseHandlingFailedIsTakenInBeforeTheIssueIsParked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{OperatorToken: true})
	number := askedIssue(t, f)
	f.Comment(number, "maintainer", "MEMBER", answer)
	f.Clock.Advance(clarifyTimeout + time.Minute)
	// A complete poll after the deadline holds the answer in the inbox, so
	// no poll is needed before parking and only intake's handling fails.
	if err := f.Control.ReconcileGitHub(ctx); err != nil {
		t.Fatal(err)
	}
	f.GitHub.FailNext(1000, http.StatusBadGateway)
	if err := f.Control.Pass(ctx); err == nil {
		t.Fatal("a pass while GitHub fails reported no error")
	}
	_ = f.Control.Pass(ctx) // the answer is not due for another try yet
	// The outage ends; the answer is tried again once its wait is over.
	f.GitHub.ClearFaults()
	for range 3 {
		f.Clock.Advance(time.Minute)
		f.Pass()
	}
	wantAnswered(t, f, number)
}

// An issue nobody answered is still parked once a backlog of inbox entries
// is handled.
func TestAnUnansweredIssueIsParkedOnceTheInboxBacklogIsHandled(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{OperatorToken: true})
	number := askedIssue(t, f)
	f.Clock.Advance(clarifyTimeout - 30*time.Second)
	f.Pass()
	openUnlabeled(f, 120)
	f.Clock.Advance(30 * time.Second)
	for range 4 {
		f.Pass()
	}
	if got := countComments(t, f, number, "Parked"); got != 1 {
		t.Fatalf("%d parking comments on the unanswered issue, want 1", got)
	}
}
