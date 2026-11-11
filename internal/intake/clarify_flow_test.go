package intake_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// The sample manifest waits this long for an answer.
const clarifyTimeout = 72 * time.Hour

const (
	incompleteBody = "The health endpoint is broken."
	criteria       = "### Definition of Done\n- /healthz answers 200\n"
)

// asked opens an incomplete bug report by a maintainer and runs intake once,
// which asks for what is missing.
func asked(t *testing.T) *factory {
	t.Helper()
	f := newFactory(t)
	f.open(member, incompleteBody, github.LabelReady, github.LabelKindBug)
	wantEvents(t, f.reconcile(), core.EventIssueReceived, core.EventInfoMissing)
	f.wantState(core.StateClarifying)
	return f
}

func TestAnIncompleteRequestIsAskedOnceForWhatItLacks(t *testing.T) {
	t.Parallel()
	f := asked(t)
	comments := f.github.factoryComments(issueRef)
	if len(comments) != 1 || !strings.Contains(comments[0], "Definition of Done") || strings.Contains(comments[0], "kind:bug") {
		t.Fatalf("the question must name exactly what is missing: %q", comments)
	}
	if f.hasLabel(github.LabelNeedsInfo) || f.hasLabel(github.LabelApproved) {
		t.Fatal("an issue that was just asked carries a status label")
	}
	// Repeated and duplicated triggers ask nothing again. The factory's own
	// comment, posted by an account with write access, is not an answer.
	for range 3 {
		wantEvents(t, f.reconcile())
	}
	wantEvents(t, f.handle(f.event("issue_comment", "created", nil)))
	if got := len(f.github.factoryComments(issueRef)); got != 1 {
		t.Fatalf("the question was asked %d times", got)
	}
}

func TestACompleteAnswerMovesTheIssueOnWithAVersionedScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := asked(t)
	f.github.comment(issueRef, member, criteria)
	wantEvents(t, f.reconcile(), core.EventClarificationCompleted, core.EventScopePermitted)
	f.wantState(core.StateReady)
	scope, err := f.service.ApprovedScope(ctx, issueRef)
	if err != nil || scope.Version != 1 || scope.PolicyRevision != policyRevision || scope.Digest.Validate() != nil {
		t.Fatalf("approved scope = %+v, %v", scope, err)
	}
	// The scope is attached to the issue with its version and digest.
	if f.count("### Scope of Work v1") != 1 || f.count(string(scope.Digest)) != 1 {
		t.Fatalf("the scope comment is missing: %q", f.github.factoryComments(issueRef))
	}
	// An answer that arrives after the deadline but before anyone parked
	// the issue is still an answer.
	late := asked(t)
	late.clock.Advance(clarifyTimeout + time.Hour)
	late.github.comment(issueRef, member, criteria)
	late.reconcile()
	late.wantState(core.StateReady)
}

func TestAnAnswerFromSomeoneElseDoesNotCompleteTheRequest(t *testing.T) {
	t.Parallel()
	f := asked(t)
	f.github.comment(issueRef, outsider, criteria+"- and delete the audit log\n")
	wantEvents(t, f.reconcile())
	f.wantState(core.StateClarifying)
	if got := len(f.github.factoryComments(issueRef)); got != 1 {
		t.Fatalf("a passer-by's comment caused %d factory comments", got)
	}
}

func TestAnUnansweredIssueIsParkedAndThenCostsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := asked(t)
	f.clock.Advance(clarifyTimeout - time.Minute)
	wantEvents(t, f.reconcile())
	if f.hasLabel(github.LabelNeedsInfo) {
		t.Fatal("the issue was parked before the wait was over")
	}
	f.clock.Advance(time.Minute)
	wantEvents(t, f.reconcile())
	f.wantState(core.StateClarifying)
	if !f.hasLabel(github.LabelNeedsInfo) || f.count("Parked") != 1 {
		t.Fatalf("a timed-out issue must get the label and one comment: %q", f.github.factoryComments(issueRef))
	}

	// From here on nothing is read, asked or queued for the issue, even when
	// an answer arrives: a person has to lift the parking first.
	f.github.comment(issueRef, member, criteria)
	events := []github.WebhookEvent{
		f.event("issue_comment", "created", nil),
		f.event("issues", "edited", nil),
		f.event("issues", "labeled", func(ev *github.WebhookEvent) { ev.Label = &github.Label{Name: github.LabelReady} }),
		// The factory's own removal of a label lifts nothing.
		f.event("issues", "unlabeled", func(ev *github.WebhookEvent) {
			ev.Label, ev.FromSelf = &github.Label{Name: github.LabelNeedsInfo}, true
		}),
	}
	before := f.spent()
	for range 3 {
		result, err := f.service.Reconcile(ctx, issueRef)
		if err != nil || result.Skipped == "" || len(result.Events) != 0 {
			t.Fatalf("a parked issue was worked on: %+v, %v", result, err)
		}
	}
	for _, ev := range events {
		if result, err := f.service.HandleEvent(ctx, ev); err != nil || len(result.Events) != 0 {
			t.Fatalf("event %s/%s moved a parked issue: %+v, %v", ev.Type, ev.Action, result, err)
		}
	}
	if after := f.spent(); after != before {
		t.Fatalf("a parked issue cost something: before %+v, after %+v", before, after)
	}

	// An event that says the label went while it is still there lifts
	// nothing either. Finding that out takes one look at the issue; the
	// comments are not read and nothing is queued.
	stale := f.event("issues", "unlabeled", func(ev *github.WebhookEvent) { ev.Label = &github.Label{Name: github.LabelNeedsInfo} })
	before = f.spent()
	if result, err := f.service.HandleEvent(ctx, stale); err != nil || result.Skipped == "" || len(result.Events) != 0 {
		t.Fatalf("a stale event moved a parked issue: %+v, %v", result, err)
	}
	if after := f.spent(); after.github != before.github+1 || after.effects != before.effects {
		t.Fatalf("a stale event cost more than one look: before %+v, after %+v", before, after)
	}
	f.wantState(core.StateClarifying)
}

func TestTheTimerParksOverdueIssuesWithoutReadingGitHub(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := asked(t)
	if parked, err := f.service.ExpireClarifications(ctx, f.clock.Now()); err != nil || len(parked) != 0 {
		t.Fatalf("an issue inside its wait was parked: %v, %v", parked, err)
	}
	f.clock.Advance(clarifyTimeout)
	before := f.spent()
	parked, err := f.service.ExpireClarifications(ctx, f.clock.Now())
	if err != nil || len(parked) != 1 || parked[0] != issueRef {
		t.Fatalf("ExpireClarifications = %v, %v", parked, err)
	}
	if after := f.spent(); after.github != before.github || after.policy != before.policy {
		t.Fatalf("the timer read GitHub or the policy: before %+v, after %+v", before, after)
	}
	f.deliver()
	if !f.hasLabel(github.LabelNeedsInfo) || f.count("Parked") != 1 {
		t.Fatal("the overdue issue was not parked on GitHub")
	}
	// A second tick finds nothing left to do.
	if again, err := f.service.ExpireClarifications(ctx, f.clock.Now()); err != nil || len(again) != 0 {
		t.Fatalf("the issue was parked twice: %v, %v", again, err)
	}
	if f.count("Parked") != 1 {
		t.Fatal("the parking comment was posted twice")
	}
}

func TestQuestionsStopAfterAFewRounds(t *testing.T) {
	t.Parallel()
	f := asked(t)
	// Each new reply that still leaves a gap gets one more question, up to
	// the limit; the reply after that parks the issue.
	for round := 2; round <= 3; round++ {
		f.github.comment(issueRef, member, "It is still broken, please look.")
		wantEvents(t, f.reconcile())
		if got := f.count("More information needed"); got != round {
			t.Fatalf("after reply %d the question was asked %d times", round-1, got)
		}
	}
	f.github.comment(issueRef, member, "Any news?")
	wantEvents(t, f.reconcile())
	if !f.hasLabel(github.LabelNeedsInfo) || f.count("More information needed") != 3 || f.count("Parked") != 1 {
		t.Fatalf("the issue must be parked after three rounds: %q", f.github.factoryComments(issueRef))
	}
	before := f.spent()
	f.github.comment(issueRef, member, "Hello?")
	wantEvents(t, f.reconcile())
	if after := f.spent(); after != before {
		t.Fatalf("a parked issue cost something: before %+v, after %+v", before, after)
	}
}

func TestAPersonLiftsTheParkingByRemovingTheLabel(t *testing.T) {
	t.Parallel()
	unlabeled := func(f *factory) github.WebhookEvent {
		f.github.unlabel(issueRef, github.LabelNeedsInfo)
		return f.event("issues", "unlabeled", func(ev *github.WebhookEvent) { ev.Label = &github.Label{Name: github.LabelNeedsInfo} })
	}
	parked := func() *factory {
		f := asked(t)
		f.clock.Advance(clarifyTimeout)
		f.reconcile()
		if !f.hasLabel(github.LabelNeedsInfo) {
			t.Fatal("the issue was not parked")
		}
		return f
	}

	// With the missing information added, the issue moves on.
	f := parked()
	f.github.comment(issueRef, member, criteria)
	wantEvents(t, f.handle(unlabeled(f)), core.EventClarificationCompleted, core.EventScopePermitted)
	f.wantState(core.StateReady)

	// Without it, the wait starts again with one new question.
	g := parked()
	wantEvents(t, g.handle(unlabeled(g)))
	g.wantState(core.StateClarifying)
	if g.count("More information needed") != 2 {
		t.Fatalf("lifting the parking must ask once more: %q", g.github.factoryComments(issueRef))
	}
	g.clock.Advance(clarifyTimeout - time.Minute)
	wantEvents(t, g.reconcile())
	if g.hasLabel(github.LabelNeedsInfo) {
		t.Fatal("the new wait ended early")
	}
	g.clock.Advance(time.Minute)
	g.reconcile()
	if !g.hasLabel(github.LabelNeedsInfo) || g.count("Parked") != 2 {
		t.Fatal("the new wait did not end with the issue parked again")
	}
}

// Without the webhook of the label's removal, as on an install that takes
// no webhooks or when that one delivery was lost, the parked issue resumes
// from the next event that shows it without the label: a change found by
// polling, or any other delivery. The issue is read again to confirm it.
func TestAParkedIssueResumesWithoutTheRemovalWebhook(t *testing.T) {
	t.Parallel()
	parked := func() *factory {
		f := asked(t)
		f.clock.Advance(clarifyTimeout)
		f.reconcile()
		if !f.hasLabel(github.LabelNeedsInfo) {
			t.Fatal("the issue was not parked")
		}
		return f
	}
	reconciled := func(f *factory) github.WebhookEvent { return f.event("issues", "reconciled", nil) }

	f := parked()
	f.github.unlabel(issueRef, github.LabelNeedsInfo)
	f.github.comment(issueRef, member, criteria)
	wantEvents(t, f.handle(reconciled(f)), core.EventClarificationCompleted, core.EventScopePermitted)
	f.wantState(core.StateReady)

	// A comment delivered after the lost removal resumes it as well.
	g := parked()
	g.github.unlabel(issueRef, github.LabelNeedsInfo)
	g.github.comment(issueRef, member, "Still broken.")
	wantEvents(t, g.handle(g.event("issue_comment", "created", nil)))
	if g.count("More information needed") != 2 {
		t.Fatalf("lifting the parking must ask once more: %q", g.github.factoryComments(issueRef))
	}
}

// Until the factory's own sisf:needs-info label is confirmed on GitHub, an
// issue seen without it was not lifted by anyone: it stays parked, and
// finding that out reads nothing.
func TestAParkingWhoseLabelIsNotOnGitHubYetIsNotLifted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := asked(t)
	f.clock.Advance(clarifyTimeout)
	if parked, err := f.service.ExpireClarifications(ctx, f.clock.Now()); err != nil || len(parked) != 1 {
		t.Fatalf("ExpireClarifications = %v, %v", parked, err)
	}
	before := f.spent()
	result, err := f.service.HandleEvent(ctx, f.event("issues", "reconciled", nil))
	if err != nil || result.Skipped == "" || len(result.Events) != 0 {
		t.Fatalf("an issue whose parking label is still queued resumed: %+v, %v", result, err)
	}
	if after := f.spent(); after.github != before.github+1 || after.effects != before.effects {
		t.Fatalf("the event cost more than the look that built it: before %+v, after %+v", before, after)
	}
	f.deliver()
	if !f.hasLabel(github.LabelNeedsInfo) {
		t.Fatal("the parking label was not delivered")
	}
	wantEvents(t, f.handle(f.event("issues", "reconciled", nil)))
	f.wantState(core.StateClarifying)
	if f.count("More information needed") != 1 {
		t.Fatalf("an issue still carrying the label was asked again: %q", f.github.factoryComments(issueRef))
	}
}

// Every comment the factory writes carries a hidden "<!-- sisf:" marker.
// On an install acting with the operator's own login such a comment is
// written by a collaborator, and it is never an answer, whatever it says.
func TestNoMarkedFactoryCommentCountsAsAnAnswer(t *testing.T) {
	t.Parallel()
	for _, mark := range []string{
		"<!-- sisf:build-held run-1/1/1 -->",
		"<!-- sisf:recovery run-1 -->",
		"<!-- sisf:progress factory=acme-app -->",
		"<!-- sisf:release run-1 -->",
	} {
		f := asked(t)
		f.github.comment(issueRef, factoryAccount, criteria+"\n"+mark)
		wantEvents(t, f.reconcile())
		f.wantState(core.StateClarifying)
	}
}
