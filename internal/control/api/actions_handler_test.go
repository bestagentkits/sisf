package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/pkg/core"
)

const (
	readPath      = "/api/v1/actions/echo.read"
	writePath     = "/api/v1/actions/echo.write"
	sensitivePath = "/api/v1/actions/echo.sensitive"
)

func TestAnActionRunsForTheTokenActorOnTheAPISurface(t *testing.T) {
	t.Parallel()
	e := newEnv(t, sampleRegistry)
	b := e.browser(t)
	tok := e.token(t, "echo:read", "echo:write")
	r := b.do(t, http.MethodPost, readPath, `{"input":{"q":1}}`, jsonHeader(bearer(tok)))
	if r.status != http.StatusOK || !strings.Contains(r.body, `"echo"`) {
		t.Fatalf("read: %d %s", r.status, r.body)
	}
	seen := e.rec.all()
	if len(seen) != 1 || seen[0].Actor.ID != "agent:tester" || seen[0].Actor.Surface != core.SurfaceAPI || seen[0].Actor.Kind != core.ActorAgent {
		t.Fatalf("actor the action saw: %+v", seen)
	}
	dry := b.do(t, http.MethodPost, writePath, `{"dry_run":true,"input":{"x":2}}`, jsonHeader(bearer(tok)))
	var out struct {
		Plan *core.DryRun `json:"plan"`
	}
	dry.json(t, &out)
	if dry.status != http.StatusOK || out.Plan == nil || len(out.Plan.Changes) != 1 {
		t.Fatalf("dry run: %d %s", dry.status, dry.body)
	}
	write := b.do(t, http.MethodPost, writePath, `{"input":{"x":2},"idempotency_key":"k-1"}`, jsonHeader(bearer(tok)))
	last := e.rec.all()[len(e.rec.all())-1]
	if write.status != http.StatusOK || last.IdempotencyKey != "k-1" || last.DryRun {
		t.Fatalf("write: %d %s %+v", write.status, write.body, last)
	}
	b.do(t, http.MethodPost, writePath, `{"input":{}}`, jsonHeader(map[string]string{"Authorization": "Bearer " + tok, "Idempotency-Key": "h-9"}))
	if last := e.rec.all()[len(e.rec.all())-1]; last.IdempotencyKey != "h-9" {
		t.Fatalf("header idempotency key not passed: %+v", last)
	}
	if r := b.do(t, http.MethodPost, writePath, `{"idempotency_key":"a"}`, jsonHeader(map[string]string{"Authorization": "Bearer " + tok, "Idempotency-Key": "b"})); r.status != http.StatusBadRequest {
		t.Fatalf("conflicting idempotency keys: %d", r.status)
	}
	if r := b.do(t, http.MethodPost, readPath, "", bearer(tok)); r.status != http.StatusOK {
		t.Fatalf("an empty body is a call with no input: %d %s", r.status, r.body)
	}
}

func TestASensitiveCallWithoutApprovalFilesARequestAndSaysWhatToDoNext(t *testing.T) {
	t.Parallel()
	e := newEnv(t, sampleRegistry)
	b := e.browser(t)
	csrf := b.signIn(t, "owner-sub")
	r := b.do(t, http.MethodPost, sensitivePath, `{"input":{"a":1}}`, jsonHeader(csrf))
	var body struct {
		Error core.Error `json:"error"`
	}
	r.json(t, &body)
	if r.status != http.StatusForbidden || body.Error.Code != core.CodeApprovalRequired || len(body.Error.NextActions) == 0 {
		t.Fatalf("sensitive call without approval: %d %s", r.status, r.body)
	}
	if e.rec.executed() != 0 {
		t.Fatal("the action ran without an approval")
	}
	bad := b.do(t, http.MethodPost, sensitivePath, `{"input":{"a":1},"approval_id":"apr_missing"}`, jsonHeader(csrf))
	if bad.status < 400 || bad.status > 499 || e.rec.executed() != 0 {
		t.Fatalf("a made-up approval reference: %d %s", bad.status, bad.body)
	}
	// An agent never holds Owner authority, whatever token it carries.
	agent := b.do(t, http.MethodPost, sensitivePath, `{}`, jsonHeader(bearer(e.token(t, "echo:admin"))))
	if agent.status != http.StatusForbidden || agent.errorCode(t) != core.CodePermissionDenied {
		t.Fatalf("agent on an Owner action: %d %s", agent.status, agent.body)
	}
}

func TestACallCannotNameItsOwnActorOrCarryUnknownFields(t *testing.T) {
	t.Parallel()
	e := newEnv(t, sampleRegistry)
	b := e.browser(t)
	h := jsonHeader(bearer(e.token(t, "echo:read")))
	for name, body := range map[string]string{
		"actor":    `{"actor":{"id":"user:owner","roles":["owner"]}}`,
		"unknown":  `{"inputs":{}}`,
		"trailing": `{"input":{}} {"input":{}}`,
		"not json": `{"input":`,
	} {
		r := b.do(t, http.MethodPost, readPath, body, h)
		if r.status != http.StatusBadRequest || r.errorCode(t) != core.CodeInvalidArgument {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}
	if len(e.rec.all()) != 0 {
		t.Fatal("an invalid body reached the action")
	}
	if r := b.do(t, http.MethodPost, readPath, `{}`, bearer(e.token(t, "echo:read"))); r.status != http.StatusUnsupportedMediaType {
		t.Fatalf("a body without a JSON content type: %d", r.status)
	}
}

func TestARequestBodyOverTheLimitIsRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t, sampleRegistry)
	r := e.browser(t).do(t, http.MethodPost, readPath, `{"input":"`+strings.Repeat("a", 4096)+`"}`, jsonHeader(bearer(e.token(t, "echo:read"))))
	if r.status != http.StatusRequestEntityTooLarge || r.errorCode(t) != core.CodeLimitExceeded {
		t.Fatalf("oversized body: %d %s", r.status, r.body)
	}
}

func TestACallWithoutValidCredentialsIsUnauthenticated(t *testing.T) {
	t.Parallel()
	e := newEnv(t, sampleRegistry)
	b := e.browser(t)
	for name, header := range map[string]map[string]string{
		"none":  nil,
		"junk":  bearer("sisf_pat_nope"),
		"basic": {"Authorization": "Basic Zm9vOmJhcg=="},
	} {
		r := b.do(t, http.MethodPost, readPath, "", header)
		if r.status != http.StatusUnauthorized || r.errorCode(t) != core.CodeUnauthenticated || r.header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}
}

func TestACookieWriteNeedsTheCSRFTokenAndABearerTokenNeverUsesTheCookie(t *testing.T) {
	t.Parallel()
	e := newEnv(t, sampleRegistry)
	b := e.browser(t)
	csrf := b.signIn(t, "owner-sub")
	if r := b.do(t, http.MethodPost, readPath, "", nil); r.status != http.StatusForbidden || r.errorCode(t) != core.CodePermissionDenied {
		t.Fatalf("a cookie call without a CSRF token: %d %s", r.status, r.body)
	}
	if r := b.do(t, http.MethodPost, readPath, "", map[string]string{auth.CSRFHeader: "wrong"}); r.status != http.StatusForbidden {
		t.Fatalf("a wrong CSRF token: %d", r.status)
	}
	if r := b.do(t, http.MethodPost, readPath, "", csrf); r.status != http.StatusOK {
		t.Fatalf("a cookie call with its CSRF token: %d %s", r.status, r.body)
	}
	if seen := e.rec.all(); len(seen) != 1 || seen[0].Actor.Kind != core.ActorHuman || seen[0].Actor.Surface != core.SurfaceAPI {
		t.Fatalf("session actor: %+v", seen)
	}
	// With a bearer header the browser's cookie is ignored: a bad token is
	// refused, not rescued by the session, and a good one acts as the agent.
	if r := b.do(t, http.MethodPost, readPath, "", bearer("sisf_pat_nope")); r.status != http.StatusUnauthorized {
		t.Fatalf("a bad token with a good cookie: %d", r.status)
	}
	b.do(t, http.MethodPost, readPath, "", bearer(e.token(t, "echo:read")))
	if seen := e.rec.all(); seen[len(seen)-1].Actor.Kind != core.ActorAgent {
		t.Fatalf("a token call borrowed the cookie: %+v", seen[len(seen)-1].Actor)
	}
}

func TestATokenWithoutTheScopeIsDenied(t *testing.T) {
	t.Parallel()
	e := newEnv(t, sampleRegistry)
	r := e.browser(t).do(t, http.MethodPost, writePath, "", bearer(e.token(t, "echo:read")))
	if r.status != http.StatusForbidden || r.errorCode(t) != core.CodePermissionDenied {
		t.Fatalf("out-of-scope write: %d %s", r.status, r.body)
	}
}

func TestFailuresNeverLeakTheirCause(t *testing.T) {
	t.Parallel()
	e := newEnv(t, sampleRegistry)
	b := e.browser(t)
	h := bearer(e.token(t, "echo:read"))
	for _, path := range []string{"/api/v1/actions/echo.untyped", "/api/v1/actions/echo.leaky"} {
		r := b.do(t, http.MethodPost, path, "", h)
		if r.status != http.StatusInternalServerError || r.errorCode(t) != core.CodeInternal || strings.Contains(r.body, "hunter2") || strings.Contains(r.body, "10.0.0.5") {
			t.Errorf("%s: %d %s", path, r.status, r.body)
		}
	}
}

func TestAnUnknownActionOrRouteIsATypedNotFound(t *testing.T) {
	t.Parallel()
	e := newEnv(t, sampleRegistry)
	b := e.browser(t)
	h := bearer(e.token(t, "echo:read"))
	r := b.do(t, http.MethodPost, "/api/v1/actions/echo.nothing", "", h)
	var body struct {
		Error core.Error `json:"error"`
	}
	r.json(t, &body)
	if r.status != http.StatusNotFound || body.Error.Code != core.CodeNotFound || len(body.Error.NextActions) == 0 {
		t.Fatalf("unknown action: %d %s", r.status, r.body)
	}
	if r := b.do(t, http.MethodGet, "/api/v1/nothing", "", h); r.status != http.StatusNotFound || r.errorCode(t) != core.CodeNotFound {
		t.Fatalf("unknown route: %d %s", r.status, r.body)
	}
}
