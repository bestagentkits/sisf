package wiring_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// answer is a complete answer to the question asked of askedIssue.
const answer = "### Definition of Done\n- /healthz answers 200\n"

// askedIssue opens an incomplete request on f and returns it once the
// factory asked for what it lacks.
func askedIssue(t *testing.T, f *harness.Factory) int {
	t.Helper()
	number := f.OpenIssue("maintainer", "MEMBER", "Health check fails", "The health endpoint is broken.", github.LabelReady, github.LabelKindBug)
	for range 2 {
		f.Pass()
	}
	if f.Record(number).State != core.StateClarifying {
		t.Fatalf("the incomplete issue is %s, want it asked", f.Record(number).State)
	}
	return number
}

// wantAnswered fails unless the answered issue moved on and was not parked.
func wantAnswered(t *testing.T, f *harness.Factory, number int) {
	t.Helper()
	if got := countComments(t, f, number, "Parked"); got != 0 {
		t.Fatal("an issue answered before its deadline was parked")
	}
	if got := f.Record(number).State; got == core.StateClarifying {
		t.Fatalf("the answered issue is still %s", got)
	}
}

// On an install without webhooks an answer that lands less than one poll
// interval before the deadline is only known once polled: the factory
// polls before it parks anything.
func TestAnAnswerJustBeforeTheDeadlineIsPolledBeforeTheIssueIsParked(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{OperatorToken: true})
	number := askedIssue(t, f)
	f.Clock.Advance(clarifyTimeout - 30*time.Second)
	f.Pass()
	f.Comment(number, "maintainer", "MEMBER", answer)
	f.Clock.Advance(30 * time.Second)
	for range 2 {
		f.Pass()
	}
	wantAnswered(t, f, number)
}

// While the operator token's access is revoked nothing is polled, so an
// answer given then is unknown when the access returns after the deadline:
// the first pass polls before it parks anything.
func TestAnAnswerGivenWhileAccessWasRevokedIsTakenInBeforeExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{OperatorToken: true})
	number := askedIssue(t, f)
	if err := f.GitHub.SetWriteAccess(f.Repo, false); err != nil {
		t.Fatal(err)
	}
	f.Clock.Advance(wiring.OperatorAccessInterval)
	if err := f.Control.Pass(ctx); !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("a pass with the write access revoked = %v, want PERMISSION_DENIED", err)
	}
	f.Comment(number, "maintainer", "MEMBER", answer)
	if err := f.GitHub.SetWriteAccess(f.Repo, true); err != nil {
		t.Fatal(err)
	}
	f.Clock.Advance(clarifyTimeout)
	for range 2 {
		f.Pass()
	}
	wantAnswered(t, f, number)
}
