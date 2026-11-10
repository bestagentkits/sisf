package control_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

func TestVerifiedWebhookIsInTheInboxBeforeItIsAcknowledged(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		rec := e.deliver(t, issueEvent{delivery: "delivery-1", action: "opened", number: 5, title: "Add export"})
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
		}
		// The response is already written, so the entry must already exist.
		pending := e.pendingDeliveries(t)
		if len(pending) != 1 {
			t.Fatalf("%d inbox entries, want 1", len(pending))
		}
		entry := pending[0]
		if entry.Source != control.InboxSourceGitHub || entry.DeliveryID != "delivery-1" || entry.EventType != "issues" {
			t.Errorf("entry = %+v", entry)
		}
		var stored github.WebhookEvent
		if err := json.Unmarshal(entry.Payload, &stored); err != nil {
			t.Fatalf("payload: %v", err)
		}
		if stored.Issue == nil || stored.Issue.Title != "Add export" || stored.Action != "opened" {
			t.Errorf("stored event = %+v", stored)
		}
	})
}

func TestWebhookWithABadSignatureStoresNothing(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		body := issueEvent{delivery: "d", action: "opened", number: 5, title: "x"}.body(t)
		other := github.Sign(core.NewSecretValue([]byte("some-other-secret")), body)
		tampered := github.Sign(core.NewSecretValue([]byte(webhookSecret)), append([]byte(nil), body[:len(body)-1]...))
		cases := map[string]string{
			"no signature":        "",
			"wrong secret":        other,
			"signature of a diff": tampered,
			"not a signature":     "sha256=zz",
			"wrong algorithm":     "sha1=" + other[len("sha256="):],
		}
		for name, signature := range cases {
			rec := e.deliverRaw("d", "issues", body, signature)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s: status = %d, want 401", name, rec.Code)
			}
		}
		if n := len(e.pendingDeliveries(t)); n != 0 {
			t.Errorf("%d inbox entries after unauthenticated deliveries, want 0", n)
		}
	})
}

func TestWebhookFromAnotherInstallationOrRepositoryIsRefused(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		var payload map[string]any
		body := issueEvent{delivery: "d", action: "opened", number: 5, title: "x"}.body(t)
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		sign := func(p map[string]any) (*httptest.ResponseRecorder, error) {
			raw, err := json.Marshal(p)
			if err != nil {
				return nil, err
			}
			return e.deliverRaw("d", "issues", raw, github.Sign(core.NewSecretValue([]byte(webhookSecret)), raw)), nil
		}
		payload["installation"] = map[string]any{"id": 1}
		if rec, err := sign(payload); err != nil || rec.Code != http.StatusForbidden {
			t.Errorf("foreign installation: %v %v, want 403", rec.Code, err)
		}
		payload["installation"] = map[string]any{"id": installationID}
		payload["repository"] = map[string]any{"name": "other", "owner": map[string]any{"login": "acme"}}
		if rec, err := sign(payload); err != nil || rec.Code != http.StatusForbidden {
			t.Errorf("foreign repository: %v %v, want 403", rec.Code, err)
		}
		if n := len(e.pendingDeliveries(t)); n != 0 {
			t.Errorf("%d inbox entries, want 0", n)
		}
	})
}

func TestDuplicateDeliveryIsStoredOnce(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		ev := issueEvent{delivery: "same-id", action: "opened", number: 5, title: "first"}
		if rec := e.deliver(t, ev); rec.Code != http.StatusAccepted {
			t.Fatalf("first = %d", rec.Code)
		}
		if rec := e.deliver(t, ev); rec.Code != http.StatusOK {
			t.Fatalf("repeat = %d, want 200 as a duplicate", rec.Code)
		}
		if n := len(e.pendingDeliveries(t)); n != 1 {
			t.Fatalf("%d inbox entries after a repeat, want 1", n)
		}
	})
}

func TestDuplicateDeliveryAfterARestartIsStillStoredOnce(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		e.deliver(t, issueEvent{delivery: "same-id", action: "opened", number: 5, title: "first"})
		// A restart empties the receiver's memory; only the database remembers.
		e.restart(t)
		rec := e.deliver(t, issueEvent{delivery: "same-id", action: "edited", number: 5, title: "changed by replay"})
		if rec.Code >= 300 {
			t.Fatalf("replay = %d, want a 2xx", rec.Code)
		}
		pending := e.pendingDeliveries(t)
		if len(pending) != 1 {
			t.Fatalf("%d inbox entries after a replay across a restart, want 1", len(pending))
		}
		var stored github.WebhookEvent
		if err := json.Unmarshal(pending[0].Payload, &stored); err != nil || stored.Issue.Title != "first" {
			t.Errorf("the first delivery must stand; got %+v (%v)", stored.Issue, err)
		}
		e.seedRecord(t, 5, toReady...)
		detail := e.issueView(t, 5)
		if detail.Title != "first" {
			t.Errorf("a replayed delivery changed the title to %q", detail.Title)
		}
	})
}

func TestEchoOfTheFactorysOwnEditIsNotStored(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		bot := map[string]any{"id": botID, "login": "sisf-factory[bot]", "type": "Bot"}
		rec := e.deliver(t, issueEvent{delivery: "echo-1", action: "labeled", number: 5, title: "x", sender: bot})
		if rec.Code >= 300 {
			t.Fatalf("echo = %d, want a 2xx so GitHub stops retrying", rec.Code)
		}
		if n := len(e.pendingDeliveries(t)); n != 0 {
			t.Fatalf("%d inbox entries for the factory's own edit, want 0", n)
		}
	})
}

func TestWebhookIsNotAcknowledgedWhenTheInboxCannotStoreIt(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		if err := e.store.Close(); err != nil {
			t.Fatal(err)
		}
		rec := e.deliver(t, issueEvent{delivery: "d-lost", action: "opened", number: 5, title: "x"})
		if rec.Code < 500 {
			t.Fatalf("status = %d, want a 5xx so GitHub retries", rec.Code)
		}
	})
}

func TestWebhookRouteOnlyTakesVerifiedPosts(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		rec := e.do(httptest.NewRequest(http.MethodGet, "/webhooks/github", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET = %d, want 405", rec.Code)
		}
		req := httptest.NewRequest(http.MethodPost, "/webhooks/github", nil)
		if rec := e.do(req); rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("POST without JSON = %d, want 415", rec.Code)
		}
	})
}

func TestStaleDeliveryDoesNotOverwriteANewerTitle(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		e.seedRecord(t, 5, toReady...)
		base := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
		e.deliver(t, issueEvent{delivery: "newer", action: "edited", number: 5, title: "Newest title", updatedAt: base.Add(time.Hour)})
		e.deliver(t, issueEvent{delivery: "older", action: "edited", number: 5, title: "Stale title", updatedAt: base})
		detail := e.issueView(t, 5)
		if detail.Title != "Newest title" {
			t.Errorf("title = %q, want the newest edit to stand", detail.Title)
		}
		if n := len(e.pendingDeliveries(t)); n != 2 {
			t.Errorf("%d inbox entries, want both deliveries kept for the consumer", n)
		}
	})
}

func TestCommentsAreProjectedEditedAndRemoved(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		e.seedRecord(t, 5, toReady...)
		comment := func(id int, body string, minute int) map[string]any {
			return map[string]any{
				"id": id, "body": body, "user": map[string]any{"id": 77, "login": "alice"},
				"created_at": time.Date(2026, 11, 10, 6, minute, 0, 0, time.UTC),
			}
		}
		send := func(delivery, action string, c map[string]any) {
			t.Helper()
			rec := e.deliver(t, issueEvent{delivery: delivery, eventType: "issue_comment", action: action, number: 5, title: "t", comment: c})
			if rec.Code != http.StatusAccepted {
				t.Fatalf("%s = %d: %s", delivery, rec.Code, rec.Body.String())
			}
		}
		detail := func() control.IssueView { return e.issueView(t, 5) }
		send("c1", "created", comment(1, "first", 1))
		send("c2", "created", comment(2, "second", 2))
		send("c1e", "edited", comment(1, "first, edited", 1))
		if got := detail().Comments; len(got) != 2 || got[0].Body != "first, edited" || got[1].Body != "second" {
			t.Fatalf("comments = %+v", got)
		}
		send("c2d", "deleted", comment(2, "second", 2))
		if got := detail().Comments; len(got) != 1 || got[0].ID != 1 {
			t.Fatalf("after delete: %+v", got)
		}
		for i := 3; i < 40; i++ {
			send("bulk-"+string(rune('a'+i%26))+string(rune('a'+i/26)), "created", comment(i, "bulk", i%50))
		}
		if got := detail().Comments; len(got) != 20 {
			t.Errorf("%d comments kept, want the newest 20", len(got))
		}
	})
}
