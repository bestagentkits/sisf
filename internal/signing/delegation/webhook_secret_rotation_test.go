package delegation

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

const testInstallation int64 = 4242

// newWebhookReceiver builds a GitHub receiver whose secrets come from ws.
func newWebhookReceiver(t *testing.T, ws *WebhookSecrets) *github.Receiver {
	t.Helper()
	r, err := github.NewReceiver(github.ReceiverConfig{
		Secrets:        ws.Secrets,
		InstallationID: testInstallation,
		Repos:          []github.RepoRef{{Owner: "acme", Name: "app"}},
		Self:           github.Identity{ID: 999, Login: "sisf-bot"},
	}, func(context.Context, github.WebhookEvent) error { return nil })
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	return r
}

// labeledIssueBody is a delivery the receiver accepts once its signature is good.
func labeledIssueBody(t *testing.T) []byte {
	t.Helper()
	user := map[string]any{"id": 77, "login": "alice", "type": "User"}
	body, err := json.Marshal(map[string]any{
		"action":       "labeled",
		"installation": map[string]any{"id": testInstallation},
		"repository":   map[string]any{"name": "app", "owner": map[string]any{"login": "acme"}},
		"sender":       user,
		"issue": map[string]any{
			"number": 5, "title": "t", "body": "b", "state": "open", "user": user, "author_association": "NONE",
			"labels": []map[string]any{{"name": "sisf:ready"}},
		},
		"label": map[string]any{"name": "sisf:ready"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return body
}

// deliver verifies one delivery signed with secret.
func deliver(t *testing.T, r *github.Receiver, secret core.SecretValue) error {
	t.Helper()
	body := labeledIssueBody(t)
	_, err := r.Verify(github.Sign(secret, body), "issues", "delivery-1", body)
	return err
}

func secret(s string) core.SecretValue { return core.NewSecretValue([]byte(s)) }

func TestRotatedWebhookSecretIsAcceptedUntilTheOverlapCloses(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	oldSecret, newSecret := secret("old-webhook-secret"), secret("new-webhook-secret")
	ws, err := NewWebhookSecrets(clock, oldSecret)
	if err != nil {
		t.Fatalf("NewWebhookSecrets: %v", err)
	}
	r := newWebhookReceiver(t, ws)
	if err := deliver(t, r, oldSecret); err != nil {
		t.Fatalf("before rotation, old secret: %v", err)
	}
	wantCode(t, deliver(t, r, newSecret), core.CodeSignatureInvalid)

	const overlap = time.Hour
	if err := ws.Rotate(newSecret, overlap); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	closes := start.Add(overlap)

	// No event is lost while GitHub switches: both secrets verify.
	for _, at := range []time.Time{start, closes.Add(-time.Nanosecond)} {
		clock.Set(at)
		if err := deliver(t, r, oldSecret); err != nil {
			t.Fatalf("old secret at %s, inside the overlap: %v", at, err)
		}
		if err := deliver(t, r, newSecret); err != nil {
			t.Fatalf("new secret at %s: %v", at, err)
		}
	}

	clock.Set(closes)
	wantCode(t, deliver(t, r, oldSecret), core.CodeSignatureInvalid)
	if err := deliver(t, r, newSecret); err != nil {
		t.Fatalf("new secret after the overlap: %v", err)
	}
	if got := len(ws.Secrets()); got != 1 {
		t.Fatalf("accepted secrets after the overlap = %d, want 1", got)
	}
}

func TestEachReplacedWebhookSecretKeepsItsOwnWindow(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	first, second, third := secret("first-secret"), secret("second-secret"), secret("third-secret")
	ws, err := NewWebhookSecrets(clock, first)
	if err != nil {
		t.Fatalf("NewWebhookSecrets: %v", err)
	}
	r := newWebhookReceiver(t, ws)
	if err := ws.Rotate(second, 2*time.Hour); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	clock.Advance(time.Hour)
	if err := ws.Rotate(third, 2*time.Hour); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	for _, s := range []core.SecretValue{first, second, third} {
		if err := deliver(t, r, s); err != nil {
			t.Fatalf("inside both windows: %v", err)
		}
	}
	clock.Advance(time.Hour) // the first window closes, the second is open
	wantCode(t, deliver(t, r, first), core.CodeSignatureInvalid)
	if err := deliver(t, r, second); err != nil {
		t.Fatalf("second secret inside its window: %v", err)
	}
	clock.Advance(time.Hour)
	wantCode(t, deliver(t, r, second), core.CodeSignatureInvalid)
	if err := deliver(t, r, third); err != nil {
		t.Fatalf("current secret: %v", err)
	}
}

func TestWebhookRotationRefusesUnsafeInput(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	current := secret("current-secret")
	ws, err := NewWebhookSecrets(clock, current)
	if err != nil {
		t.Fatalf("NewWebhookSecrets: %v", err)
	}
	cases := map[string]struct {
		next    core.SecretValue
		overlap time.Duration
	}{
		"empty secret":          {core.SecretValue{}, time.Hour},
		"no overlap":            {secret("next-secret"), 0},
		"negative overlap":      {secret("next-secret"), -time.Minute},
		"endless overlap":       {secret("next-secret"), MaxWebhookOverlap + time.Second},
		"secret already in use": {secret("current-secret"), time.Hour},
	}
	for name, tc := range cases {
		if err := ws.Rotate(tc.next, tc.overlap); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: error = %v, want INVALID_ARGUMENT", name, err)
		}
	}
	if got := ws.Secrets(); len(got) != 1 {
		t.Fatalf("a refused rotation changed the accepted secrets: %d", len(got))
	}
	if _, err := NewWebhookSecrets(clock, core.SecretValue{}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("empty initial secret: error = %v", err)
	}
	if _, err := NewWebhookSecrets(nil, current); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("no clock: error = %v", err)
	}
}

func TestWebhookSecretsAreSafeForConcurrentUse(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	ws, err := NewWebhookSecrets(clock, secret("secret-0"))
	if err != nil {
		t.Fatalf("NewWebhookSecrets: %v", err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = ws.Rotate(secret("secret-"+string(rune('a'+i))), time.Minute) // a duplicate is refused, which is fine here
		}()
		go func() {
			defer wg.Done()
			if len(ws.Secrets()) == 0 {
				t.Error("no secret accepted during rotation")
			}
		}()
	}
	wg.Wait()
}
