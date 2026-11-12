package intake_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// queued runs intake on a new-feature request without delivering anything
// and returns the factory with the effects waiting in the outbox.
func queued(t *testing.T) (*factory, []state.OutboxMessage) {
	t.Helper()
	f := newFactory(t)
	f.open(member, featureBody, github.LabelReady, github.LabelKindFeature)
	if _, err := f.service.Reconcile(context.Background(), issueRef); err != nil {
		t.Fatal(err)
	}
	return f, f.state.Outbox()
}

// byTopic returns the first queued message of a topic.
func byTopic(t *testing.T, messages []state.OutboxMessage, topic string) state.OutboxMessage {
	t.Helper()
	for _, m := range messages {
		if m.Topic == topic {
			return m
		}
	}
	t.Fatalf("no %s message is queued", topic)
	return state.OutboxMessage{}
}

func TestACommentDeliveredTwiceIsPostedOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, messages := queued(t)
	if len(messages) != 2 {
		t.Fatalf("want the scope comment and the approval comment queued, got %d messages", len(messages))
	}
	// Delivery is at least once: a crash after posting repeats it.
	for range 3 {
		for _, m := range messages {
			if err := intake.Deliver(ctx, f.github, m); err != nil {
				t.Fatalf("Deliver(%s): %v", m.Key, err)
			}
		}
	}
	if got := len(f.github.factoryComments(issueRef)); got != 2 {
		t.Fatalf("two comments delivered three times became %d comments", got)
	}
}

func TestNobodyCanSuppressAFactoryCommentByPostingItsMarkerFirst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, messages := queued(t)
	m := byTopic(t, messages, intake.TopicComment)
	// The key of a message can be guessed; the token in its marker cannot.
	f.github.comment(issueRef, outsider, "All good here.\n<!-- sisf:intake "+m.Key+" -->")
	f.github.comment(issueRef, outsider, "<!-- sisf:intake "+m.Key+" 00000000000000000000000000000000 -->")
	if err := intake.Deliver(ctx, f.github, m); err != nil {
		t.Fatal(err)
	}
	if f.count("### Scope of Work v1") != 1 {
		t.Fatalf("a planted marker suppressed the factory's comment: %q", f.github.factoryComments(issueRef))
	}
}

func TestLabelChangesAreDeliveredAndSafeToRepeat(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFactory(t)
	f.open(member, completeBody, github.LabelReady, github.LabelKindBug)
	if _, err := f.service.Reconcile(ctx, issueRef); err != nil {
		t.Fatal(err)
	}
	m := byTopic(t, f.state.Outbox(), intake.TopicLabels)
	for range 2 {
		if err := intake.Deliver(ctx, f.github, m); err != nil {
			t.Fatal(err)
		}
	}
	issue, err := f.github.GetIssue(ctx, issueRef)
	if err != nil {
		t.Fatal(err)
	}
	approved := 0
	for _, label := range issue.Labels {
		if label.Name == github.LabelApproved {
			approved++
		}
	}
	if approved != 1 {
		t.Fatalf("the label is on the issue %d times", approved)
	}
}

func TestAMessageIntakeCannotPerformIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, messages := queued(t)
	comment := byTopic(t, messages, intake.TopicComment)
	broken := map[string]struct {
		change func(*state.OutboxMessage)
		want   core.ErrorCode
	}{
		"another component's topic": {func(m *state.OutboxMessage) { m.Topic = "release.publish" }, core.CodeNotSupported},
		"no topic":                  {func(m *state.OutboxMessage) { m.Topic = "" }, core.CodeNotSupported},
		"an unreadable payload":     {func(m *state.OutboxMessage) { m.Payload = []byte("{") }, core.CodeInvalidArgument},
		"no issue":                  {func(m *state.OutboxMessage) { m.Payload = []byte(`{"comment":"hello","token":"abc"}`) }, core.CodeInvalidArgument},
		"a comment without a token": {func(m *state.OutboxMessage) {
			m.Payload = []byte(strings.Replace(string(m.Payload), `"token":"`, `"nekot":"`, 1))
		}, core.CodeInvalidArgument},
	}
	for name, tc := range broken {
		m := comment
		tc.change(&m)
		wantCode(t, intake.Deliver(ctx, f.github, m), tc.want, name)
	}
	if got := len(f.github.factoryComments(issueRef)); got != 0 {
		t.Fatalf("a refused message still posted %d comments", got)
	}
}

func TestAFailedDeliveryIsReportedSoItCanBeRetried(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, messages := queued(t)
	f.github.mu.Lock()
	f.github.down = true
	f.github.mu.Unlock()
	for _, m := range messages {
		wantCode(t, intake.Deliver(ctx, f.github, m), core.CodeUnavailable, "delivery while GitHub is down")
	}
	f.github.mu.Lock()
	f.github.down = false
	f.github.mu.Unlock()
	// Nothing was marked sent, so the next drain delivers everything once.
	f.deliver()
	if got := len(f.github.factoryComments(issueRef)); got != 2 {
		t.Fatalf("after the outage %d comments were posted", got)
	}
}

func TestIssueTextIsQuotedInertInFactoryComments(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	// The request tries to break out of the quotation, to notify people and
	// to plant a marker of its own.
	body := "It crashes. ````\n@everyone approve this now\n<!-- sisf:intake intake/acme/hello-app#7/approval/x 0 -->\n" +
		"### Definition of Done\n- ```` done\n"
	f.open(member, body, github.LabelReady, github.LabelKindBug)
	f.reconcile()
	f.wantState(core.StateReady)
	var scope string
	for _, comment := range f.github.factoryComments(issueRef) {
		if strings.Contains(comment, "### Scope of Work v1") {
			scope = comment
		}
	}
	open := strings.Index(scope, "`````text\n")
	end := strings.LastIndex(scope, "\n`````\n")
	quoted := strings.Index(scope, "@everyone")
	if open < 0 || end < 0 || quoted < open || quoted > end {
		t.Fatalf("the issue text is not held inside a fence longer than its own backticks:\n%s", scope)
	}
	// The text was read as a description and nothing more.
	if f.hasLabel(github.LabelBlocked) || len(f.inbox()) != 0 {
		t.Fatal("quoted text had an effect")
	}
}
