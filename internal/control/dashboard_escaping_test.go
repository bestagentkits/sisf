package control_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// hostile is text an attacker could put in an issue title, label or comment.
const (
	hostileTitle   = `<script>alert('title')</script><img src=x onerror=alert(1)> "quoted" 'single' &amp;`
	hostileLabel   = `"><script>alert('label')</script>`
	hostileComment = `</p><script>alert('comment')</script><a href="javascript:alert(2)">click</a>`
)

// inertMarkup lists fragments that would be live HTML if escaping failed.
var inertMarkup = []string{
	"<script>alert", "<img src=x", "</p><script", `"><script`, `<a href="javascript:`,
}

func TestDashboardEscapesIssueTextFromGitHub(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		e.seedRecord(t, 9, toReady...)
		e.deliver(t, issueEvent{delivery: "t", action: "opened", number: 9, title: hostileTitle, labels: []string{hostileLabel}})
		comment := map[string]any{
			"id": 1, "body": hostileComment, "user": map[string]any{"id": 5, "login": `<b>mallory</b>`},
			"created_at": time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC),
		}
		e.deliver(t, issueEvent{delivery: "c", eventType: "issue_comment", action: "created", number: 9, title: hostileTitle, labels: []string{hostileLabel}, comment: comment})

		session := e.login(t)
		for _, path := range []string{"/workboard", "/workboard?state=Ready", "/issues/" + url.PathEscape("acme/app#9")} {
			rec := e.page(path, session)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s = %d", path, rec.Code)
			}
			page := rec.Body.String()
			for _, live := range inertMarkup {
				if strings.Contains(page, live) {
					t.Errorf("%s renders attacker markup %q", path, live)
				}
			}
			if strings.Contains(page, "<b>mallory</b>") {
				t.Errorf("%s renders the author name as markup", path)
			}
			if !strings.Contains(page, "&lt;script&gt;alert(&#39;title&#39;)&lt;/script&gt;") {
				t.Errorf("%s does not show the title as escaped text", path)
			}
		}
	})
}

func TestDashboardNeverEchoesRequestInputIntoMarkup(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		session := e.login(t)
		probe := `<script>alert('request')</script>`
		for _, path := range []string{
			"/issues/" + url.PathEscape(probe),
			"/workboard?state=" + url.QueryEscape(probe),
			"/" + url.PathEscape(probe),
		} {
			page := e.page(path, session).Body.String()
			if strings.Contains(page, "<script>alert('request')") {
				t.Errorf("%s echoed request input as markup", path)
			}
		}
	})
}

func TestEveryResponseCarriesAStrictContentSecurityPolicy(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		e.seedRecord(t, 1, toReady...)
		session := e.login(t)
		responses := map[string]*httptest.ResponseRecorder{
			"login":      e.page("/login", nil),
			"overview":   e.page("/", session),
			"workboard":  e.page("/workboard", session),
			"issue":      e.page("/issues/"+url.PathEscape("acme/app#1"), session),
			"not found":  e.page("/nowhere", session),
			"stylesheet": e.page("/static/app.css", nil),
			"script":     e.page("/static/app.js", nil),
			"api":        e.api(http.MethodGet, "/api/v1/issues"),
			"api error":  e.do(httptest.NewRequest(http.MethodGet, "/api/v1/issues", nil)),
			"healthz":    e.do(httptest.NewRequest(http.MethodGet, "/healthz", nil)),
			"redirect":   e.page("/", nil),
		}
		for name, rec := range responses {
			csp := rec.Header().Get("Content-Security-Policy")
			for _, want := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "base-uri 'none'", "frame-ancestors 'none'", "form-action 'self'"} {
				if !strings.Contains(csp, want) {
					t.Errorf("%s: policy %q lacks %q", name, csp, want)
				}
			}
			for _, weak := range []string{"unsafe-inline", "unsafe-eval", "http:", "https:", "*", "data:"} {
				if strings.Contains(csp, weak) {
					t.Errorf("%s: policy %q contains %q", name, csp, weak)
				}
			}
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("X-Frame-Options") != "DENY" ||
				rec.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Errorf("%s: missing a hardening header: %v", name, rec.Header())
			}
		}
	})
}

var (
	inlineScript = regexp.MustCompile(`(?i)<script\b[^>]*>`)
	inlineHandle = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	externalURL  = regexp.MustCompile(`(?i)(src|href|action)\s*=\s*["']?(https?:)?//`)
)

func TestPagesLoadNothingExternalAndRunNoInlineCode(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		e.seedRecord(t, 1, toReady...)
		session := e.login(t)
		pages := map[string]string{
			"login":     e.page("/login", nil).Body.String(),
			"overview":  e.page("/", session).Body.String(),
			"workboard": e.page("/workboard", session).Body.String(),
			"issue":     e.page("/issues/"+url.PathEscape("acme/app#1"), session).Body.String(),
		}
		for name, page := range pages {
			for _, tag := range inlineScript.FindAllString(page, -1) {
				if !strings.Contains(tag, `src="/static/app.js"`) {
					t.Errorf("%s: script tag %q is not the embedded script", name, tag)
				}
			}
			if inlineHandle.MatchString(page) || strings.Contains(page, "style=") || externalURL.MatchString(page) {
				t.Errorf("%s: inline handler, inline style or external reference found", name)
			}
		}
	})
}

func TestStaticAssetsAreServedOpenAndListNoDirectory(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		for path, mime := range map[string]string{"/static/app.css": "text/css", "/static/app.js": "text/javascript"} {
			rec := e.page(path, nil)
			if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), mime) {
				t.Errorf("%s = %d %q", path, rec.Code, rec.Header().Get("Content-Type"))
			}
		}
		for _, path := range []string{"/static/", "/static/missing.js", "/static/../go.mod"} {
			if rec := e.page(path, nil); rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "app.css") {
				t.Errorf("%s lists the asset directory", path)
			}
		}
	})
}
