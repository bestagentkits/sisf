package control_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
)

// loginRequest builds the dashboard login form post.
func loginRequest(token string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{"token": {token}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func TestDashboardSendsVisitorsWithoutASessionToTheLogin(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		e.seedRecord(t, 1, toReady...)
		for _, path := range []string{"/", "/workboard", "/issues/acme%2Fapp%231", "/nowhere"} {
			rec := e.page(path, nil)
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
				t.Errorf("%s: status %d to %q, want 303 to /login", path, rec.Code, rec.Header().Get("Location"))
			}
			if strings.Contains(rec.Body.String(), "acme") {
				t.Errorf("%s leaked data to a visitor without a session", path)
			}
		}
		forged := &http.Cookie{Name: "sisf_session", Value: "forged-session-id"}
		if rec := e.page("/", forged); rec.Code != http.StatusSeeOther {
			t.Errorf("forged session: status %d, want 303", rec.Code)
		}
	})
}

func TestLoginRefusesAWrongTokenAndSetsNoCookie(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		for name, token := range map[string]string{"wrong": strings.Repeat("a", 44), "empty": "", "prefix": ownerToken[:20]} {
			rec := e.do(loginRequest(token))
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s token: status %d, want 401", name, rec.Code)
			}
			if len(rec.Result().Cookies()) != 0 {
				t.Errorf("%s token: a cookie was set", name)
			}
		}
	})
}

func TestLoginRefusesAnOversizedForm(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		rec := e.do(loginRequest(strings.Repeat("a", 64<<10)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status %d, want 400", rec.Code)
		}
	})
}

func TestSessionCookieIsHardened(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		c := e.login(t)
		if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
			t.Errorf("cookie = %+v, want HttpOnly, SameSite=Strict, Path=/", c)
		}
		if rec := e.page("/", c); rec.Code != http.StatusOK {
			t.Errorf("overview with a session = %d, want 200", rec.Code)
		}
	})
}

func TestSessionExpiresWithTheClock(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		session := e.login(t)
		e.db.Clock.Advance(control.DefaultSessionTTL - time.Minute)
		if rec := e.page("/", session); rec.Code != http.StatusOK {
			t.Fatalf("before expiry: %d, want 200", rec.Code)
		}
		e.db.Clock.Advance(2 * time.Minute)
		if rec := e.page("/", session); rec.Code != http.StatusSeeOther {
			t.Fatalf("after expiry: %d, want 303", rec.Code)
		}
	})
}

func TestLogoutEndsTheSession(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		session := e.login(t)
		req := httptest.NewRequest(http.MethodPost, "/logout", nil)
		req.AddCookie(session)
		if rec := e.do(req); rec.Code != http.StatusSeeOther {
			t.Fatalf("logout = %d, want 303", rec.Code)
		}
		if rec := e.page("/", session); rec.Code != http.StatusSeeOther {
			t.Fatalf("the old session still works after logout: %d", rec.Code)
		}
	})
}

func TestRestartLogsEveryoneOut(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		session := e.login(t)
		e.restart(t)
		if rec := e.page("/", session); rec.Code != http.StatusSeeOther {
			t.Fatalf("a session survived a restart: %d", rec.Code)
		}
	})
}

func TestSessionCookieIsSecureBehindATLSProxy(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		req := loginRequest(ownerToken)
		req.Header.Set("X-Forwarded-Proto", "https")
		cookies := e.do(req).Result().Cookies()
		if len(cookies) != 1 || !cookies[0].Secure {
			t.Fatalf("cookies = %+v, want one Secure cookie", cookies)
		}
	})
}
