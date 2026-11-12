package github_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

func TestDeliveryFromAnotherInstallationIsForbidden(t *testing.T) {
	t.Parallel()
	in := &inbox{}
	r := newReceiver(t, in)
	body := payload(t, "labeled", map[string]any{"installation": map[string]any{"id": 999}})
	if got := status(t, r, signed(body, "issues", "d-1")); got != http.StatusForbidden {
		t.Fatalf("status %d, want 403", got)
	}
	body = payload(t, "labeled", map[string]any{"installation": nil})
	if got := status(t, r, signed(body, "issues", "d-2")); got != http.StatusBadRequest {
		t.Fatalf("a payload without an installation: status %d, want 400", got)
	}
	if len(in.got()) != 0 {
		t.Fatal("a foreign delivery reached the sink")
	}
}

func TestDeliveryForAnotherRepositoryIsForbidden(t *testing.T) {
	t.Parallel()
	in := &inbox{}
	r := newReceiver(t, in)
	other := map[string]any{"name": "secrets", "owner": map[string]any{"login": "acme"}}
	if got := status(t, r, signed(payload(t, "labeled", map[string]any{"repository": other}), "issues", "d-1")); got != http.StatusForbidden {
		t.Fatalf("other repository: status %d, want 403", got)
	}
	stranger := map[string]any{"name": "app", "owner": map[string]any{"login": "mallory"}}
	if got := status(t, r, signed(payload(t, "labeled", map[string]any{"repository": stranger}), "issues", "d-2")); got != http.StatusForbidden {
		t.Fatalf("same name, other owner: status %d, want 403", got)
	}
	if len(in.got()) != 0 {
		t.Fatal("a delivery for a repository the factory does not serve reached the sink")
	}
	cased := map[string]any{"name": "APP", "owner": map[string]any{"login": "ACME"}}
	if got := status(t, r, signed(payload(t, "labeled", map[string]any{"repository": cased}), "issues", "d-3")); got != http.StatusAccepted {
		t.Fatalf("GitHub names are case-insensitive: status %d, want 202", got)
	}
}

func TestVerifiedDeliveriesThatAreNotWorkAreAcknowledgedAndIgnored(t *testing.T) {
	t.Parallel()
	in := &inbox{}
	r := newReceiver(t, in)
	cases := map[string]delivery{
		"ping":            signed([]byte(`{"zen":"Keep it logically awesome."}`), "ping", "d-1"),
		"unhandled type":  signed(payload(t, "created", nil), "star", "d-2"),
		"unhandled actio": signed(payload(t, "assigned", nil), "issues", "d-3"),
	}
	for name, d := range cases {
		rec := post(t, r, d)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ignored") {
			t.Errorf("%s: %d %s, want 200 ignored", name, rec.Code, rec.Body)
		}
	}
	if len(in.got()) != 0 {
		t.Fatal("an event the factory does not react to reached the sink")
	}
}

func TestMalformedDeliveriesAreRejected(t *testing.T) {
	t.Parallel()
	in := &inbox{}
	r := newReceiver(t, in, func(c *github.ReceiverConfig) { c.MaxBodyBytes = 4096 })
	good := payload(t, "labeled", nil)
	cases := map[string]struct {
		d    delivery
		want int
	}{
		"not json":                        {signed([]byte(`{nope`), "issues", "d-1"), http.StatusBadRequest},
		"no delivery id":                  {signed(good, "issues", ""), http.StatusBadRequest},
		"hostile delivery id":             {signed(good, "issues", "../../etc/passwd"), http.StatusBadRequest},
		"no event type":                   {signed(good, "", "d-2"), http.StatusBadRequest},
		"issue event without an issue":    {signed(payload(t, "labeled", map[string]any{"issue": nil}), "issues", "d-3"), http.StatusBadRequest},
		"comment event without a comment": {signed(payload(t, "created", nil), "issue_comment", "d-4"), http.StatusBadRequest},
		"pull request event without one":  {signed(payload(t, "opened", nil), "pull_request", "d-5"), http.StatusBadRequest},
		"too large":                       {signed(append([]byte(`{"x":"`), make([]byte, 8192)...), "issues", "d-6"), http.StatusRequestEntityTooLarge},
		"wrong content type": {delivery{body: good, event: "issues", id: "d-7", signature: github.Sign(core.NewSecretValue([]byte(testSecret)), good),
			mutate: func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }}, http.StatusUnsupportedMediaType},
	}
	for name, c := range cases {
		if got := status(t, r, c.d); got != c.want {
			t.Errorf("%s: status %d, want %d", name, got, c.want)
		}
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/webhook", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status %d, want 405", rec.Code)
	}
	if len(in.got()) != 0 {
		t.Fatal("a malformed delivery reached the sink")
	}
}

func TestReplayedDeliveryIsAcknowledgedAndNotProcessedTwice(t *testing.T) {
	t.Parallel()
	in := &inbox{}
	r := newReceiver(t, in)
	d := signed(payload(t, "labeled", nil), "issues", "d-1")
	if got := status(t, r, d); got != http.StatusAccepted {
		t.Fatalf("first delivery: %d", got)
	}
	rec := post(t, r, d)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "duplicate") {
		t.Fatalf("replay: %d %s, want 200 duplicate", rec.Code, rec.Body)
	}
	if got := len(in.got()); got != 1 {
		t.Fatalf("sink received %d events for one delivery id", got)
	}
	other := signed(payload(t, "labeled", nil), "issues", "d-2")
	if status(t, r, other) != http.StatusAccepted || len(in.got()) != 2 {
		t.Fatal("a different delivery id must be processed")
	}
}

func TestDeliveryIsRetryableWhenTheInboxFailed(t *testing.T) {
	t.Parallel()
	in := &inbox{fail: errInboxDown}
	r := newReceiver(t, in)
	d := signed(payload(t, "labeled", nil), "issues", "d-1")
	rec := post(t, r, d)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("failed inbox: %d, want 503 with Retry-After", rec.Code)
	}
	if strings.Contains(rec.Body.String(), errInboxDown.Error()) {
		t.Fatal("the response leaks the inbox error")
	}
	in.mu.Lock()
	in.fail = nil
	in.mu.Unlock()
	if got := status(t, r, d); got != http.StatusAccepted || len(in.got()) != 1 {
		t.Fatalf("redelivery after failure: %d with %d events; a failed delivery must not be remembered as handled", got, len(in.got()))
	}
}

func TestSameDeliveryInFlightIsNotHandledConcurrently(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	r, err := github.NewReceiver(github.ReceiverConfig{Secrets: secrets(testSecret), InstallationID: testInstall, Repos: []github.RepoRef{repoRef}, Self: self},
		func(context.Context, github.WebhookEvent) error {
			if calls.Add(1) == 1 {
				close(entered)
				<-release
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	d := signed(payload(t, "labeled", nil), "issues", "d-1")
	first := make(chan int, 1)
	go func() { first <- status(t, r, d) }()
	<-entered
	if rec := post(t, r, d); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("same delivery while in flight: %d, want 503 so it is retried later", rec.Code)
	}
	close(release)
	if got := <-first; got != http.StatusAccepted {
		t.Fatalf("first delivery: %d", got)
	}
	if rec := post(t, r, d); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "duplicate") {
		t.Fatalf("after completion: %d %s, want 200 duplicate", rec.Code, rec.Body)
	}
	if calls.Load() != 1 {
		t.Fatalf("sink ran %d times", calls.Load())
	}
}
