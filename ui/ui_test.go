package ui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// pageData is the shape the layout reads; the control server supplies the same
// fields on its own page data.
type pageData struct {
	Nav      string
	LoggedIn bool
	Message  string
}

func TestEveryPageParses(t *testing.T) {
	t.Parallel()
	if _, err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestRenderRefusesAnUnknownPage(t *testing.T) {
	t.Parallel()
	pages, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pages.Render("missing", pageData{}); err == nil {
		t.Fatal("rendering an unknown page succeeded")
	}
}

func TestRenderEscapesTextInEveryContext(t *testing.T) {
	t.Parallel()
	pages, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	out, err := pages.Render(PageError, pageData{Message: `</p><script>alert(1)</script>`, LoggedIn: true, Nav: `"><script>alert(2)</script>`})
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

var forbidden = map[string]*regexp.Regexp{
	"an inline script":    regexp.MustCompile(`(?i)<script\b(?:[^>]*\s)?>|<script>`),
	"an event handler":    regexp.MustCompile(`(?i)\son[a-z]+\s*=`),
	"an inline style":     regexp.MustCompile(`(?i)\sstyle\s*=|<style\b`),
	"an external address": regexp.MustCompile(`(?i)(?:src|href|action)\s*=\s*["']?(?:https?:)?//|url\(\s*["']?https?:|@import|\bfetch\(|XMLHttpRequest|eval\(`),
	"a javascript URL":    regexp.MustCompile(`(?i)javascript:`),
}

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
			for what, pattern := range forbidden {
				// The embedded script is the one allowed script reference.
				text := strings.ReplaceAll(string(raw), `<script src="/static/app.js" defer></script>`, "")
				if pattern.MatchString(text) {
					t.Errorf("%s/%s contains %s", dir, entry.Name(), what)
				}
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
