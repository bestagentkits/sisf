package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func serve(t *testing.T, h http.Handler, method, target string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec.Code, rec.Body.String()
}

func TestHealthzReportsOKWhileTheDataVolumeIsWritable(t *testing.T) {
	t.Parallel()
	app, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if code, body := serve(t, app.handler(), http.MethodGet, "/healthz"); code != http.StatusOK || !strings.Contains(body, `"ok"`) {
		t.Fatalf("GET /healthz = %d %q, want 200 ok", code, body)
	}
}

func TestHealthzFailsWhenTheDataVolumeCannotBeWritten(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	app, err := newApp(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if code, _ := serve(t, app.handler(), http.MethodGet, "/healthz"); code != http.StatusServiceUnavailable {
		t.Fatalf("GET /healthz with a missing data volume = %d, want 503", code)
	}
}

func TestVisitsAreCountedAndSurviveARestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first, err := newApp(dir)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		serve(t, first.handler(), http.MethodGet, "/")
	}
	// A new process on the same volume continues from the stored count.
	second, err := newApp(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, body := serve(t, second.handler(), http.MethodGet, "/"); !strings.Contains(body, "visit 4") {
		t.Fatalf("page after restart = %q, want it to say visit 4", body)
	}
	raw, err := os.ReadFile(filepath.Join(dir, visitsFile))
	if err != nil || strings.TrimSpace(string(raw)) != "4" {
		t.Fatalf("stored count = %q (%v), want 4", raw, err)
	}
}

func TestMetricsExposeCountersInTextFormat(t *testing.T) {
	t.Parallel()
	app, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := app.handler()
	serve(t, h, http.MethodGet, "/")
	code, body := serve(t, h, http.MethodGet, "/metrics")
	if code != http.StatusOK {
		t.Fatalf("GET /metrics = %d", code)
	}
	for _, want := range []string{"hello_up 1", "hello_visits_total 1", "hello_http_requests_total"} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q:\n%s", want, body)
		}
	}
}

func TestOnlyReadMethodsAreServedAndUnknownPathsAreNotFound(t *testing.T) {
	t.Parallel()
	app, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := app.handler()
	if code, _ := serve(t, h, http.MethodPost, "/"); code != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", code)
	}
	if code, _ := serve(t, h, http.MethodGet, "/admin"); code != http.StatusNotFound {
		t.Errorf("GET /admin = %d, want 404", code)
	}
}

func TestNewAppRefusesAnUnusableDataDirectory(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newApp(file); err == nil {
		t.Fatal("a regular file was accepted as the data directory")
	}
	if _, err := newApp(""); err == nil {
		t.Fatal("an empty data directory was accepted")
	}
}
