package fakegithub_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/bestagentkits/sisf/internal/testkit/fakegithub"
)

const (
	testRepo   = "acme/app"
	testSecret = "fake-webhook-secret"
	testToken  = "fake-token"
)

// received is a webhook as the receiving end saw it.
type received struct {
	ID, Event, Action string
	SignatureOK       bool
}

// sink is an httptest receiver that records deliveries.
type sink struct {
	mu   sync.Mutex
	got  []received
	code int
}

func (k *sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write(body)
	var p struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal(body, &p)
	k.mu.Lock()
	k.got = append(k.got, received{
		ID: r.Header.Get("X-GitHub-Delivery"), Event: r.Header.Get("X-GitHub-Event"), Action: p.Action,
		SignatureOK: r.Header.Get("X-Hub-Signature-256") == "sha256="+hex.EncodeToString(mac.Sum(nil)),
	})
	code := k.code
	k.mu.Unlock()
	if code == 0 {
		code = http.StatusAccepted
	}
	w.WriteHeader(code)
}

func (k *sink) deliveries() []received {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]received(nil), k.got...)
}

// env is a fake with one repository and a webhook pointed at a sink.
type env struct {
	fake *fakegithub.Server
	api  *httptest.Server
	hook *sink
}

func newEnv(t *testing.T, opts ...fakegithub.Option) *env {
	t.Helper()
	fake := fakegithub.New(opts...)
	if err := fake.AddRepo(testRepo); err != nil {
		t.Fatal(err)
	}
	hook := &sink{}
	hookSrv := httptest.NewServer(hook)
	api := httptest.NewServer(fake)
	t.Cleanup(func() { hookSrv.Close(); api.Close() })
	fake.SetWebhook(hookSrv.URL, testSecret)
	return &env{fake: fake, api: api, hook: hook}
}

// call performs an API request as the bot (or as actor when given) and
// returns the status and body.
func (e *env) call(t *testing.T, method, path string, body any, headers ...string) (int, []byte, http.Header) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, e.api.URL+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out, resp.Header
}

func actions(got []received) []string {
	out := make([]string, len(got))
	for i, g := range got {
		out[i] = g.Event + "." + g.Action
	}
	return out
}

func stringsReader(s string) io.Reader { return bytes.NewBufferString(s) }
