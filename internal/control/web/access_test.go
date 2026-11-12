package web_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func defaultFixtures() *fixtures {
	return &fixtures{hostile: hostileText, budgetConfirmed: true, githubRef: true, sample: true}
}

// pages are the paths of every dashboard page.
var pages = []string{"/", "/workboard", "/runs?issue=acme/app%231", "/runs", "/telemetry", "/approvals", "/product", "/security", "/setup", "/blocked?issue=acme/app%231"}

func TestUnauthenticatedRequestsAreSentToSignIn(t *testing.T) {
	t.Parallel()
	e := newEnv(t, defaultFixtures())
	b := e.browser(t)
	for _, path := range pages {
		r := b.get(t, path)
		loc := r.header.Get("Location")
		if r.status != http.StatusSeeOther || !strings.HasPrefix(loc, "/signin?return_to=") {
			t.Errorf("GET %s = %d to %q, want a redirect to the sign-in page", path, r.status, loc)
		}
		if strings.Contains(r.body, "factory") && strings.Contains(r.body, "acme-app") {
			t.Errorf("GET %s leaked page content", path)
		}
	}
	post := b.postRaw(t, "/actions/factory.emergency_stop", url.Values{"reason": {"x"}})
	if post.status != http.StatusSeeOther || post.header.Get("Location") != "/signin" {
		t.Errorf("POST without a session = %d to %q, want the sign-in page", post.status, post.header.Get("Location"))
	}
	if n := e.seen.ran("dashboard"); n != 0 {
		t.Errorf("%d actions ran for an unauthenticated visitor", n)
	}
	page := b.get(t, "/signin?return_to=/workboard")
	if page.status != http.StatusOK || !strings.Contains(page.body, `href="/auth/login/google?return_to=%2fworkboard"`) {
		t.Errorf("sign-in page = %d: %.300s", page.status, page.body)
	}
}

func TestSignInReturnTargetStaysOnThisServer(t *testing.T) {
	t.Parallel()
	b := newEnv(t, defaultFixtures()).browser(t)
	for _, target := range []string{"https://evil.test/", "//evil.test/", "/\\evil.test", "/\t/evil.test", "/%09/evil.test"} {
		page := b.get(t, "/signin?return_to="+url.QueryEscape(target))
		if strings.Contains(page.body, "evil.test") {
			t.Errorf("return_to %q is offered to the provider", target)
		}
	}
}

func TestSignedInPersonSeesEveryPageUnderTheStrictPolicy(t *testing.T) {
	t.Parallel()
	b := newEnv(t, defaultFixtures()).browser(t)
	b.signIn(t, ownerSub)
	handler := regexp.MustCompile(`(?i)<[a-z][^>]*\s(on[a-z]+|style)\s*=`)
	for _, path := range pages {
		r := b.get(t, path)
		if r.status != http.StatusOK {
			t.Errorf("GET %s = %d: %.300s", path, r.status, r.body)
			continue
		}
		csp := r.header.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "base-uri 'none'", "frame-ancestors 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("GET %s: policy %q lacks %q", path, csp, want)
			}
		}
		if strings.Contains(csp, "unsafe") {
			t.Errorf("GET %s: policy allows unsafe sources: %s", path, csp)
		}
		if handler.MatchString(r.body) {
			t.Errorf("GET %s holds an inline handler or style", path)
		}
		for _, tag := range regexp.MustCompile(`<script[^>]*>`).FindAllString(r.body, -1) {
			if !strings.Contains(tag, "src=") {
				t.Errorf("GET %s holds an inline script %s", path, tag)
			}
		}
		if path == "/" && !strings.Contains(r.body, `<script src="/static/webmcp.js" defer>`) {
			t.Errorf("the layout does not load the WebMCP script")
		}
		if r.header.Get("Cache-Control") != "no-store" || r.header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s lacks the cache and sniffing headers", path)
		}
	}
	if css := b.get(t, "/static/app.css"); css.status != http.StatusOK || css.header.Get("Content-Security-Policy") == "" {
		t.Errorf("static asset = %d", css.status)
	}
	if dir := b.get(t, "/static/"); dir.status != http.StatusNotFound {
		t.Errorf("static directory listing = %d", dir.status)
	}
	if missing := b.get(t, "/nope"); missing.status != http.StatusNotFound {
		t.Errorf("unknown page = %d", missing.status)
	}
}

func TestBearerTokenDoesNotActThroughTheDashboard(t *testing.T) {
	t.Parallel()
	b := newEnv(t, defaultFixtures()).browser(t)
	b.signIn(t, ownerSub)
	r := b.do(t, http.MethodGet, "/", "", map[string]string{"Authorization": "Bearer something"})
	if r.status != http.StatusSeeOther || !strings.HasPrefix(r.header.Get("Location"), "/signin") {
		t.Errorf("a request with a token and a cookie = %d, want the sign-in page", r.status)
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	t.Parallel()
	b := newEnv(t, defaultFixtures()).browser(t)
	b.signIn(t, ownerSub)
	if r := b.postRaw(t, "/logout", url.Values{}); r.status != http.StatusForbidden {
		t.Errorf("logout without the CSRF token = %d, want 403", r.status)
	}
	if r := b.post(t, "/logout", url.Values{}); r.status != http.StatusSeeOther || r.header.Get("Location") != "/signin" {
		t.Errorf("logout = %d to %q", r.status, r.header.Get("Location"))
	}
	if r := b.get(t, "/"); r.status != http.StatusSeeOther {
		t.Errorf("a page after logout = %d, want a redirect to sign-in", r.status)
	}
}
