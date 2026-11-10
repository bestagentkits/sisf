// Package ui holds the dashboard: html/template pages and a little vanilla
// JavaScript, embedded in the binary. There is no build step and nothing is
// loaded from outside the server, which is what lets the Content-Security-
// Policy forbid every external origin and every inline script.
//
// The pages render structs handed in by the control server; this package
// does not fetch or compute anything. html/template escapes each value for
// the place it lands in, which is how issue titles and comments, untrusted
// text from GitHub, stay inert.
package ui

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed templates/*.html
var templateFiles embed.FS

//go:embed static/*
var staticFiles embed.FS

// Pages renders the dashboard pages. Build one with LoadDashboard.
type Pages struct {
	pages map[string]*template.Template
}

// Render returns the finished page. Rendering to memory first means a failure
// halfway never sends a truncated page.
func (p *Pages) Render(page string, data any) ([]byte, error) {
	t, ok := p.pages[page]
	if !ok {
		return nil, fmt.Errorf("unknown dashboard page %q", page)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		return nil, fmt.Errorf("render dashboard page %s: %w", page, err)
	}
	return buf.Bytes(), nil
}

// StaticHandler serves the embedded stylesheet and script under /static/.
// It lists no directories.
func StaticHandler() http.Handler {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		// The directory is embedded at compile time; this cannot happen.
		panic(err)
	}
	files := http.StripPrefix("/static/", http.FileServerFS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// formatUTC shows an instant in UTC, the zone the server stores and signs in.
func formatUTC(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}

// formatISO renders an instant for a datetime attribute.
func formatISO(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
