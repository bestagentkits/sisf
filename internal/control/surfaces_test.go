package control_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

func TestEverySurfaceIsMountedAndAuthenticatesItsCaller(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		for _, c := range []struct {
			method, path string
			want         int
		}{
			{http.MethodGet, "/auth/session", http.StatusUnauthorized},
			{http.MethodPost, "/api/v1/actions/factory.status", http.StatusUnauthorized},
			{http.MethodPost, "/mcp", http.StatusUnauthorized},
			{http.MethodGet, "/.well-known/oauth-authorization-server", http.StatusOK},
			{http.MethodGet, "/.well-known/oauth-protected-resource/mcp", http.StatusOK},
			{http.MethodGet, "/webmcp/actions.json", http.StatusOK},
			{http.MethodGet, "/static/webmcp.js", http.StatusOK},
			{http.MethodGet, "/", http.StatusSeeOther},
			{http.MethodGet, "/approvals", http.StatusSeeOther},
			{http.MethodGet, "/healthz", http.StatusOK},
		} {
			rec := e.do(httptest.NewRequest(c.method, c.path, strings.NewReader("{}")))
			if rec.Code != c.want {
				t.Errorf("%s %s = %d, want %d: %s", c.method, c.path, rec.Code, c.want, rec.Body.String())
			}
		}
	})
}

func TestTheWebMCPToolListIsTheRegistry(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		rec := e.do(httptest.NewRequest(http.MethodGet, "/webmcp/actions.json", nil))
		var list struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		getJSON(t, rec, &list)
		var names []string
		for _, tool := range list.Tools {
			names = append(names, tool.Name)
		}
		var want []string
		for _, spec := range e.deps.Surfaces.Actions.Specs() {
			want = append(want, spec.Name)
		}
		if !slices.Equal(names, want) {
			t.Fatalf("tools %v, registry %v", names, want)
		}
	})
}

// A request with an Authorization header is judged by its bearer token
// alone: a browser session riding along on the same request adds nothing.
func TestAPIIgnoresADashboardSession(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		session := e.signIn(t, ownerSubject)
		withCookie := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
		withCookie.AddCookie(session)
		if rec := e.do(withCookie); rec.Code != http.StatusOK {
			t.Fatalf("the session itself is not live: %d", rec.Code)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/actions/factory.status", strings.NewReader(`{"input":{}}`))
		req.Header.Set("Authorization", "Bearer sisf_pat_not-a-real-token")
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(session)
		rec := e.do(req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("a bad bearer with a live session = %d, want 401: %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Error *core.Error `json:"error"`
		}
		getJSON(t, rec, &body)
		if body.Error == nil || body.Error.Code != core.CodeUnauthenticated {
			t.Fatalf("error = %+v", body.Error)
		}
	})
}

// The session also cannot write over REST without its CSRF token, so a page
// on another site cannot use it.
func TestASessionWritesOnlyWithItsCSRFToken(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		session := e.signIn(t, ownerSubject)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/actions/factory.status", strings.NewReader(`{"input":{}}`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(session)
		if rec := e.do(req); rec.Code != http.StatusForbidden {
			t.Fatalf("a session without its CSRF token = %d, want 403: %s", rec.Code, rec.Body.String())
		}
		view := httptest.NewRequest(http.MethodGet, auth.SessionPath, nil)
		view.AddCookie(session)
		var sv struct {
			CSRF string `json:"csrf_token"`
		}
		if err := json.Unmarshal(e.do(view).Body.Bytes(), &sv); err != nil {
			t.Fatal(err)
		}
		req = httptest.NewRequest(http.MethodPost, "/api/v1/actions/factory.status", strings.NewReader(`{"input":{}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(auth.CSRFHeader, sv.CSRF)
		req.AddCookie(session)
		if rec := e.do(req); rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
			t.Fatalf("a session with its CSRF token was refused: %d %s", rec.Code, rec.Body.String())
		}
	})
}

func TestTheServerRefusesToRunWithoutItsSurfaces(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e := newEnv(t, db)
		deps := e.deps
		deps.Surfaces = control.Surfaces{}
		if _, err := control.New(e.cfg, deps); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Fatalf("a server without surfaces: %v", err)
		}
	})
}
