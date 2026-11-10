package control_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

const (
	ownerToken     = "owner-token-0123456789abcdef0123456789abcdef"
	webhookSecret  = "hook-secret-for-tests"
	installationID = 4242
	botID          = 900001
)

// env is one control server over a real temporary store and a fake clock.
type env struct {
	db     *sqlstoretest.Database
	store  *sqlstore.Store
	server *control.Server
	// handler is the server's complete handler; requests go straight to it.
	handler http.Handler
	probes  *probeSet
	cfg     control.Config
	deps    control.Deps
}

// probeSet lets a test choose each zone's probe answer while the server runs.
type probeSet struct {
	mu      sync.Mutex
	answers map[core.Zone]func(context.Context) error
}

func (p *probeSet) set(zone core.Zone, fn func(context.Context) error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.answers[zone] = fn
}

func (p *probeSet) probe(zone core.Zone) control.HealthProbe {
	return control.HealthProbeFunc(func(ctx context.Context) error {
		p.mu.Lock()
		fn := p.answers[zone]
		p.mu.Unlock()
		return fn(ctx)
	})
}

func healthy(context.Context) error { return nil }

// withServer runs fn once per available store dialect with a migrated store
// and a server whose probes all answer.
func withServer(t *testing.T, fn func(t *testing.T, e *env)) {
	t.Helper()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		fn(t, newEnv(t, db))
	})
}

func newEnv(t *testing.T, db *sqlstoretest.Database, tweak ...func(*control.Config)) *env {
	t.Helper()
	probes := &probeSet{answers: map[core.Zone]func(context.Context) error{
		core.ZoneApplication: healthy, core.ZoneAgent: healthy,
	}}
	e := &env{db: db, store: db.Open(t, core.ZoneControl), probes: probes}
	e.cfg = control.Config{
		FactoryID:  sqlstoretest.Factory,
		OwnerToken: core.NewSecretValue([]byte(ownerToken)),
		Webhook: github.ReceiverConfig{
			Secrets:        func() []core.SecretValue { return []core.SecretValue{core.NewSecretValue([]byte(webhookSecret))} },
			InstallationID: installationID,
			Repos:          []github.RepoRef{{Owner: "acme", Name: "app"}},
			Self:           github.Identity{ID: botID, Login: "sisf-factory[bot]"},
		},
	}
	for _, f := range tweak {
		f(&e.cfg)
	}
	e.deps = control.Deps{
		Store: e.store, Clock: db.Clock,
		Probes: map[core.Zone]control.HealthProbe{
			core.ZoneApplication: probes.probe(core.ZoneApplication),
			core.ZoneAgent:       probes.probe(core.ZoneAgent),
		},
	}
	e.restart(t)
	return e
}

// restart builds a fresh server on the same store, as a process restart
// would: receiver memory and dashboard sessions are gone, the database stays.
func (e *env) restart(t *testing.T) {
	t.Helper()
	server, err := control.New(e.cfg, e.deps)
	if err != nil {
		t.Fatalf("build the control server: %v", err)
	}
	e.server, e.handler = server, server.Handler()
}

// do sends a request straight to the handler.
func (e *env) do(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// api sends an authenticated API request and returns the recorder.
func (e *env) api(method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	return e.do(req)
}

// getJSON decodes the body of rec into out.
func getJSON(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

// wantAPIError checks the status and the typed error in the body.
func wantAPIError(t *testing.T, rec *httptest.ResponseRecorder, status int, code core.ErrorCode) core.Error {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, status, rec.Body.String())
	}
	var env struct {
		Error *core.Error `json:"error"`
	}
	getJSON(t, rec, &env)
	if env.Error == nil || env.Error.Code != code {
		t.Fatalf("error = %+v, want code %s", env.Error, code)
	}
	return *env.Error
}

// login performs the dashboard login and returns the session cookie.
func (e *env) login(t *testing.T) *http.Cookie {
	t.Helper()
	form := url.Values{"token": {ownerToken}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := e.do(req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, want 303", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "sisf_session" {
			return c
		}
	}
	t.Fatal("login set no session cookie")
	return nil
}

// page fetches a dashboard page with the session cookie.
func (e *env) page(path string, session *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if session != nil {
		req.AddCookie(session)
	}
	return e.do(req)
}

// issueRef is the n-th issue of the served repository.
func issueRef(n int) core.IssueRef { return core.IssueRef{Owner: "acme", Repo: "app", Number: n} }

// seedRecord creates the lifecycle record of issue n and walks it along the
// events, so a test can place an issue in any state without caring how.
func (e *env) seedRecord(t *testing.T, n int, events ...core.Event) {
	t.Helper()
	ref := issueRef(n)
	ctx := context.Background()
	err := e.store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		rec, err := tx.Transition(ctx, core.Transition{
			IssueID: ref, To: core.StateIntake, Event: core.EventIssueReceived,
			IdempotencyKey: fmt.Sprintf("seed-%d-0", n), ActorID: "test",
		})
		for i, ev := range events {
			if err != nil {
				return err
			}
			rec, err = step(ctx, tx, rec, ev, fmt.Sprintf("seed-%d-%d", n, i+1))
		}
		return err
	})
	if err != nil {
		t.Fatalf("seed issue %d: %v", n, err)
	}
}

// step takes a record one edge further, the way the factory writes it. The
// claim takes the lease on the issue and starts a run at its first attempt;
// every later step of that run is written under the lease and names the run.
// A step that starts no run keeps the attempt the record has.
func step(ctx context.Context, tx state.Tx, rec core.StateRecord, event core.Event, key string) (core.StateRecord, error) {
	next, err := statemachine.Next(rec.State, event)
	if err != nil {
		return rec, err
	}
	write := core.Transition{
		IssueID: rec.IssueID, From: rec.State, To: next, Event: event, ExpectedVersion: rec.Version,
		IdempotencyKey: key, ActorID: "test", Attempt: rec.Attempt, RunID: rec.RunID,
	}
	switch {
	case event == core.EventClaimed:
		lease, err := tx.AcquireLease(ctx, state.IssueLeaseResource(rec.IssueID), "test-worker", time.Hour)
		if err != nil {
			return rec, err
		}
		write.LeaseOwner, write.FencingToken = lease.Owner, lease.Token
		write.RunID, write.Attempt = core.RunID(fmt.Sprintf("run-%d-%d", rec.IssueID.Number, rec.Version)), 1
	case statemachine.RequiresLease(rec.State):
		write.LeaseOwner, write.FencingToken = rec.LeaseOwner, rec.FencingToken
	}
	return tx.Transition(ctx, write)
}

// toReady and toDeveloping are the event paths tests walk most.
var (
	toReady      = []core.Event{core.EventInfoSufficient, core.EventScopePermitted}
	toDeveloping = []core.Event{core.EventInfoSufficient, core.EventScopePermitted, core.EventClaimed, core.EventDevelopmentStarted}
)

// pendingDeliveries reads the inbox through the store.
func (e *env) pendingDeliveries(t *testing.T) []state.InboxEntry {
	t.Helper()
	var out []state.InboxEntry
	err := e.store.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		out, err = tx.PendingDeliveries(ctx, 100)
		return err
	})
	if err != nil {
		t.Fatalf("read the inbox: %v", err)
	}
	return out
}

// issueEvent describes one issues or issue_comment delivery to send.
type issueEvent struct {
	delivery  string
	eventType string
	action    string
	number    int
	title     string
	labels    []string
	updatedAt time.Time
	sender    map[string]any
	comment   map[string]any
}

// body renders the GitHub payload for the event.
func (ev issueEvent) body(t *testing.T) []byte {
	t.Helper()
	user := map[string]any{"id": 77, "login": "alice", "type": "User"}
	sender := ev.sender
	if sender == nil {
		sender = user
	}
	labels := []map[string]any{}
	for _, l := range ev.labels {
		labels = append(labels, map[string]any{"name": l})
	}
	updated := ev.updatedAt
	if updated.IsZero() {
		updated = time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
	}
	payload := map[string]any{
		"action":       ev.action,
		"installation": map[string]any{"id": installationID},
		"repository":   map[string]any{"name": "app", "owner": map[string]any{"login": "acme"}},
		"sender":       sender,
		"issue": map[string]any{
			"number": ev.number, "title": ev.title, "body": "details", "state": "open", "user": user,
			"author_association": "NONE", "labels": labels, "updated_at": updated,
		},
	}
	if ev.comment != nil {
		payload["comment"] = ev.comment
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// deliver posts a signed webhook and returns the response.
func (e *env) deliver(t *testing.T, ev issueEvent) *httptest.ResponseRecorder {
	t.Helper()
	if ev.eventType == "" {
		ev.eventType = "issues"
	}
	body := ev.body(t)
	return e.deliverRaw(ev.delivery, ev.eventType, body, github.Sign(core.NewSecretValue([]byte(webhookSecret)), body))
}

// deliverRaw posts a webhook with an explicit signature header.
func (e *env) deliverRaw(delivery, eventType string, body []byte, signature string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(github.EventHeader, eventType)
	req.Header.Set(github.DeliveryHeader, delivery)
	if signature != "" {
		req.Header.Set(github.SignatureHeader, signature)
	}
	return e.do(req)
}

// advance takes issue n one step along event, from whatever state it is in.
func (e *env) advance(t *testing.T, n int, event core.Event) {
	t.Helper()
	ref := issueRef(n)
	err := e.store.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		rec, err := tx.GetRecord(ctx, ref)
		if err != nil {
			return err
		}
		_, err = step(ctx, tx, rec, event, fmt.Sprintf("advance-%d-%d", n, rec.Version))
		return err
	})
	if err != nil {
		t.Fatalf("advance issue %d by %s: %v", n, event, err)
	}
}
