package ui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// errorData is the shape the error page and its layout read; the control
// server supplies the same fields.
type errorData struct {
	LoggedIn       bool
	Nav, Who, CSRF string
	Code, Message  string
	Next           []struct{ Action, Link, Description, Params string }
}

func TestRenderRefusesAnUnknownPage(t *testing.T) {
	t.Parallel()
	pages, err := LoadDashboard()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pages.Render("missing", errorData{}); err == nil {
		t.Fatal("rendering an unknown page succeeded")
	}
}

func TestRenderEscapesTextInEveryContext(t *testing.T) {
	t.Parallel()
	pages, err := LoadDashboard()
	if err != nil {
		t.Fatal(err)
	}
	out, err := pages.Render(PageDashError, errorData{Message: `</p><script>alert(1)</script>`, Code: `"><script>alert(2)</script>`,
		LoggedIn: true, Who: `"><script>alert(3)</script>`})
	if err != nil {
		t.Fatal(err)
	}
	page := string(out)
	for _, live := range []string{"<script>alert", "</p><script"} {
		if strings.Contains(page, live) {
			t.Errorf("page contains live markup %q", live)
		}
	}
	if !strings.Contains(page, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("the message is not shown as escaped text")
	}
}

// The layout carries the WebMCP script and the hidden element it reports
// its state in.
func TestTheLayoutCarriesTheWebMCPScriptAndItsHiddenStatus(t *testing.T) {
	t.Parallel()
	pages, err := LoadDashboard()
	if err != nil {
		t.Fatal(err)
	}
	out, err := pages.Render(PageDashError, errorData{Code: "NOT_FOUND"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{webMCPScriptTag, `data-webmcp-status hidden`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the page lacks %s", want)
		}
	}
}

var forbidden = map[string]*regexp.Regexp{
	"an inline script":    regexp.MustCompile(`(?i)<script\b(?:[^>]*\s)?>|<script>`),
	"an event handler":    regexp.MustCompile(`(?i)\son[a-z]+\s*=`),
	"an inline style":     regexp.MustCompile(`(?i)\sstyle\s*=|<style\b`),
	"an external address": regexp.MustCompile(`(?i)(?:src|href|action)\s*=\s*["']?(?:https?:)?//|url\(\s*["']?https?:|@import|XMLHttpRequest|eval\(`),
	"a javascript URL":    regexp.MustCompile(`(?i)javascript:`),
}

// networkCall is any way a script reaches the network. Only the WebMCP
// script may, and only to this site's own paths (see the test below).
var networkCall = regexp.MustCompile(`\bfetch\b|XMLHttpRequest|WebSocket|EventSource|sendBeacon`)

// The two script references the layout carries.
const (
	appScriptTag    = `<script src="/static/app.js" defer></script>`
	webMCPScriptTag = `<script src="/static/webmcp.js" defer></script>`
)

// webMCPScript is the one asset allowed to call fetch.
const webMCPScript = "static/webmcp.js"

func TestAssetsLoadNothingExternalAndRunNoInlineCode(t *testing.T) {
	t.Parallel()
	for dir, files := range map[string]fs.FS{"templates": templateFiles, "static": staticFiles} {
		entries, err := fs.ReadDir(files, dir)
		if err != nil || len(entries) == 0 {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, entry := range entries {
			raw, err := fs.ReadFile(files, dir+"/"+entry.Name())
			if err != nil {
				t.Fatal(err)
			}
			// The embedded scripts are the only allowed script references.
			text := strings.ReplaceAll(strings.ReplaceAll(string(raw), appScriptTag, ""), webMCPScriptTag, "")
			for what, pattern := range forbidden {
				if pattern.MatchString(text) {
					t.Errorf("%s/%s contains %s", dir, entry.Name(), what)
				}
			}
			if path := dir + "/" + entry.Name(); path != webMCPScript && networkCall.MatchString(text) {
				t.Errorf("%s makes a network call; only %s may", path, webMCPScript)
			}
		}
	}
}

func TestStaticHandlerServesAssetsOnly(t *testing.T) {
	t.Parallel()
	h := StaticHandler()
	for path, want := range map[string]int{
		"/static/app.css":    http.StatusOK,
		"/static/app.js":     http.StatusOK,
		"/static/webmcp.js":  http.StatusOK,
		"/static/":           http.StatusNotFound,
		"/static/missing.js": http.StatusNotFound,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("%s = %d, want %d", path, rec.Code, want)
		}
	}
}

// The WebMCP script calls fetch only through send, which keeps every
// request on this site, with the session's cookies and without following a
// redirect elsewhere.
func TestTheWebMCPScriptFetchesOnlyThisSite(t *testing.T) {
	t.Parallel()
	raw, err := fs.ReadFile(staticFiles, webMCPScript)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if n := strings.Count(text, "window.fetch"); n != 2 || !strings.Contains(text, "window.fetch.bind(window)") {
		t.Errorf("%s reaches fetch outside its one guarded binding", webMCPScript)
	}
	for _, guard := range []string{"if (!isLocalPath(path))", "init.credentials = 'same-origin'", "init.redirect = 'error'"} {
		if !strings.Contains(text, guard) {
			t.Errorf("%s lacks the guard %q", webMCPScript, guard)
		}
	}
	if strings.Count(text, "request(") != 1 {
		t.Errorf("%s calls the bound fetch outside send", webMCPScript)
	}
}
