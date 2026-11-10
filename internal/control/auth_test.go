package control_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/pkg/core"
)

// apiRoutes are the routes that must refuse a caller without the Owner token.
var apiRoutes = []string{
	"/api/v1/factory/status",
	"/api/v1/issues",
	"/api/v1/issues/acme%2Fapp%231",
	"/api/v1/not-a-route",
}

func TestAPIRefusesRequestsWithoutTheOwnerToken(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		e.seedRecord(t, 1, toReady...)
		credentials := map[string]func(*http.Request){
			"no credential":       func(*http.Request) {},
			"wrong token":         func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+strings.Repeat("x", len(ownerToken))) },
			"truncated token":     func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+ownerToken[:len(ownerToken)-1]) },
			"token with a suffix": func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+ownerToken+"x") },
			"empty bearer":        func(r *http.Request) { r.Header.Set("Authorization", "Bearer ") },
			"wrong scheme":        func(r *http.Request) { r.Header.Set("Authorization", "Basic "+ownerToken) },
			"token in the query":  func(r *http.Request) { r.URL.RawQuery = "token=" + ownerToken },
			"token in a cookie":   func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "sisf_session", Value: ownerToken}) },
		}
		for _, route := range apiRoutes {
			for name, apply := range credentials {
				req := httptest.NewRequest(http.MethodGet, route, nil)
				apply(req)
				rec := e.do(req)
				wantAPIError(t, rec, http.StatusUnauthorized, core.CodeUnauthenticated)
				if rec.Header().Get("WWW-Authenticate") == "" {
					t.Errorf("%s with %s: 401 without a WWW-Authenticate challenge", route, name)
				}
				if strings.Contains(rec.Body.String(), "acme") {
					t.Errorf("%s with %s: the refusal leaked data: %s", route, name, rec.Body.String())
				}
			}
		}
	})
}

func TestAPIAcceptsTheOwnerToken(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		e.seedRecord(t, 1, toReady...)
		for _, route := range apiRoutes[:3] {
			if rec := e.api(http.MethodGet, route); rec.Code != http.StatusOK {
				t.Errorf("%s = %d, want 200: %s", route, rec.Code, rec.Body.String())
			}
		}
		wantAPIError(t, e.api(http.MethodGet, "/api/v1/not-a-route"), http.StatusNotFound, core.CodeNotFound)
	})
}

func TestAPIIgnoresADashboardSession(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		session := e.login(t)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/factory/status", nil)
		req.AddCookie(session)
		wantAPIError(t, e.do(req), http.StatusUnauthorized, core.CodeUnauthenticated)
	})
}

func TestHealthEndpointNeedsNoToken(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		rec := e.do(httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("healthz = %d, want 200: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestServerRefusesAnUnusableOwnerToken(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		for name, token := range map[string]string{"empty": "", "short": "short-token", "one byte shy": ownerToken[:control.MinTokenBytes-1]} {
			cfg := e.cfg
			cfg.OwnerToken = core.NewSecretValue([]byte(token))
			if _, err := control.New(cfg, e.deps); !core.HasCode(err, core.CodeInvalidArgument) {
				t.Errorf("%s token: error = %v, want INVALID_ARGUMENT", name, err)
			}
		}
	})
}

func TestTokenNeverAppearsInResponses(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		e.seedRecord(t, 1, toReady...)
		session := e.login(t)
		if session.Value == ownerToken {
			t.Fatal("the session cookie carries the Owner token")
		}
		responses := map[string]*httptest.ResponseRecorder{
			"login page":   e.page("/login", nil),
			"overview":     e.page("/", session),
			"workboard":    e.page("/workboard", session),
			"api status":   e.api(http.MethodGet, "/api/v1/factory/status"),
			"api issues":   e.api(http.MethodGet, "/api/v1/issues"),
			"healthz":      e.do(httptest.NewRequest(http.MethodGet, "/healthz", nil)),
			"api refusal":  e.do(httptest.NewRequest(http.MethodGet, "/api/v1/issues", nil)),
			"bad login":    e.do(loginRequest("wrong-token-wrong-token-wrong-token-0000")),
			"good login":   e.do(loginRequest(ownerToken)),
			"static asset": e.page("/static/app.js", nil),
		}
		for name, rec := range responses {
			dump := rec.Body.String()
			for k, v := range rec.Header() {
				dump += k + ": " + strings.Join(v, ",") + "\n"
			}
			if strings.Contains(dump, ownerToken) {
				t.Errorf("%s exposes the Owner token", name)
			}
		}
	})
}
