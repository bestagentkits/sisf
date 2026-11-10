package github_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// inbox is a Sink that records what it was given.
type inbox struct {
	mu     sync.Mutex
	events []github.WebhookEvent
	fail   error
}

func (i *inbox) sink(_ context.Context, ev github.WebhookEvent) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.fail != nil {
		return i.fail
	}
	i.events = append(i.events, ev)
	return nil
}

func (i *inbox) got() []github.WebhookEvent {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]github.WebhookEvent(nil), i.events...)
}

func secrets(values ...string) func() []core.SecretValue {
	return func() []core.SecretValue {
		out := make([]core.SecretValue, len(values))
		for i, v := range values {
			out[i] = core.NewSecretValue([]byte(v))
		}
		return out
	}
}

func newReceiver(t *testing.T, in *inbox, tweak ...func(*github.ReceiverConfig)) *github.Receiver {
	t.Helper()
	cfg := github.ReceiverConfig{Secrets: secrets(testSecret), InstallationID: testInstall, Repos: []github.RepoRef{repoRef}, Self: self}
	for _, f := range tweak {
		f(&cfg)
	}
	r, err := github.NewReceiver(cfg, in.sink)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// payload builds a webhook body. The fields map overrides the defaults.
func payload(t *testing.T, action string, fields map[string]any) []byte {
	t.Helper()
	user := map[string]any{"id": 77, "login": "alice", "type": "User"}
	body := map[string]any{
		"action":       action,
		"installation": map[string]any{"id": testInstall},
		"repository":   map[string]any{"name": "app", "owner": map[string]any{"login": "acme"}},
		"sender":       user,
		"issue": map[string]any{
			"number": 5, "title": "t", "body": "b", "state": "open", "user": user, "author_association": "NONE",
			"labels": []map[string]any{{"name": "sisf:ready"}},
		},
		"label": map[string]any{"name": "sisf:ready"},
	}
	for k, v := range fields {
		if v == nil {
			delete(body, k)
			continue
		}
		body[k] = v
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// delivery is a request to the receiver.
type delivery struct {
	body      []byte
	signature string
	event     string
	id        string
	mutate    func(*http.Request)
}

func signed(body []byte, event, id string) delivery {
	return delivery{body: body, event: event, id: id, signature: github.Sign(core.NewSecretValue([]byte(testSecret)), body)}
}

func post(t *testing.T, h http.Handler, d delivery) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(d.body))
	req.Header.Set("Content-Type", "application/json")
	if d.signature != "" {
		req.Header.Set(github.SignatureHeader, d.signature)
	}
	if d.event != "" {
		req.Header.Set(github.EventHeader, d.event)
	}
	if d.id != "" {
		req.Header.Set(github.DeliveryHeader, d.id)
	}
	if d.mutate != nil {
		d.mutate(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func status(t *testing.T, h http.Handler, d delivery) int { t.Helper(); return post(t, h, d).Code }

var errInboxDown = errors.New("inbox store unavailable")
