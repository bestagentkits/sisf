package github_test

import (
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/pkg/github"
)

// wire connects the fake's webhooks to a receiver, the way a deployment does.
func wire(t *testing.T, f *factory) *inbox {
	t.Helper()
	in := &inbox{}
	srv := httptest.NewServer(newReceiver(t, in))
	t.Cleanup(srv.Close)
	f.fake.SetWebhook(srv.URL, testSecret)
	return in
}

func types(events []github.WebhookEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Type + "." + e.Action
	}
	return out
}

func TestLabelTheFactorySetsComesBackAsAnEchoButAPersonsLabelDoesNot(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	in := wire(t, f)
	alice := f.person("alice")
	n := mustOpen(t, f, alice, "idea")
	if err := f.client.AddLabels(bg, repoRef.Issue(n), github.LabelRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := newProgress(f, "f1").Upsert(bg, repoRef.Issue(n), "Claimed"); err != nil {
		t.Fatal(err)
	}
	if err := f.fake.AddLabels(testRepo, n, alice, github.LabelReady); err != nil {
		t.Fatal(err)
	}
	events := in.got()
	if want := []string{"issues.opened", "issues.labeled", "issue_comment.created", "issues.labeled"}; !slices.Equal(types(events), want) {
		t.Fatalf("events = %v, want %v", types(events), want)
	}
	selfFlags := []bool{events[0].FromSelf, events[1].FromSelf, events[2].FromSelf, events[3].FromSelf}
	if !slices.Equal(selfFlags, []bool{false, true, true, false}) {
		t.Fatalf("FromSelf = %v; only the factory's own label and comment are echoes", selfFlags)
	}
}

func TestDuplicatedAndReorderedWebhooksAreHandledOnceEach(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	in := wire(t, f)
	alice := f.person("alice")
	f.fake.DuplicateNext(3)
	f.fake.Hold()
	n := mustOpen(t, f, alice, "idea")
	_ = f.fake.AddLabels(testRepo, n, alice, github.LabelReady)
	_, _ = f.fake.AddComment(testRepo, n, alice, "details")
	f.fake.Release(true)

	events := in.got()
	if want := []string{"issue_comment.created", "issues.labeled", "issues.opened"}; !slices.Equal(types(events), want) {
		t.Fatalf("events = %v, want each exactly once, newest first", types(events))
	}
	for _, d := range f.fake.Deliveries() {
		if d.Status != 202 && d.Status != 200 {
			t.Errorf("delivery %s status %d", d.ID, d.Status)
		}
	}
}

func TestDroppedWebhookIsRecoveredByRedeliveryOrByPolling(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	in := wire(t, f)
	f.fake.DropNext(1)
	n := mustOpen(t, f, f.person("alice"), "idea", github.LabelReady)
	if len(in.got()) != 0 {
		t.Fatal("a dropped delivery arrived")
	}
	// Reconciliation finds the issue although no webhook announced it.
	found, err := github.NewPoller(f.client, repoRef, zeroTime).Poll(bg)
	if err != nil || len(found) != 1 || found[0].Ref.Number != n {
		t.Fatalf("poll = %v, %v", numbers(found), err)
	}
	// Redelivery brings the original event, with its original delivery id.
	dropped := f.fake.Deliveries()[0]
	if err := f.fake.Redeliver(dropped.ID); err != nil {
		t.Fatal(err)
	}
	events := in.got()
	if len(events) != 1 || events[0].DeliveryID != dropped.ID || events[0].Action != "opened" {
		t.Fatalf("redelivered events = %+v", events)
	}
	if err := f.fake.Redeliver(dropped.ID); err != nil || len(in.got()) != 1 {
		t.Fatalf("a second redelivery must be acknowledged as a duplicate, not processed again: %v, %d", err, len(in.got()))
	}
}
