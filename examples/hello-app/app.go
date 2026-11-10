package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// visitsFile is where the visit count lives on the data volume.
const visitsFile = "visits"

// probeFile is written and removed by the health check to prove the volume
// still accepts writes.
const probeFile = ".healthz"

// app is the Zone 1 sample application: a page that counts visits on a
// writable data volume, a health check and metrics. It talks to nothing
// outside its own volume.
type app struct {
	dir      string
	cfg      settings
	logs     io.Writer
	mu       sync.Mutex // serializes the read-increment-write of the count and the fault cadence
	visits   int64
	faultAcc int
	requests atomic.Int64
}

// option changes how an app is built.
type option func(*app)

// withSettings sets the revision and the injected faults.
func withSettings(s settings) option { return func(a *app) { a.cfg = s } }

// withLogs sets where the app writes its log lines; they are dropped by default.
func withLogs(w io.Writer) option { return func(a *app) { a.logs = w } }

// newApp opens the data volume and loads the stored visit count.
func newApp(dir string, opts ...option) (*app, error) {
	if dir == "" {
		return nil, errors.New("the data directory is not set")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("data directory: %w", err)
	}
	a := &app{dir: dir, logs: io.Discard}
	for _, o := range opts {
		o(a)
	}
	raw, err := os.ReadFile(filepath.Join(dir, visitsFile))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("read visit count: %w", err)
	default:
		if a.visits, err = strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err != nil || a.visits < 0 {
			return nil, errors.New("the stored visit count is not a number; remove the file to start from zero")
		}
	}
	return a, nil
}

// handler returns the routes of the application.
func (a *app) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.page)
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("GET /metrics", a.metrics)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.requests.Add(1)
		mux.ServeHTTP(w, r)
	})
}

// page counts the visit durably and says which one it was.
func (a *app) page(w http.ResponseWriter, _ *http.Request) {
	if a.injectedError() {
		http.Error(w, "injected failure", http.StatusInternalServerError)
		return
	}
	n, err := a.countVisit()
	if err != nil {
		http.Error(w, "the visit could not be stored", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintf(w, "Hello from the sample app: visit %d\n", n)
}

// countVisit adds one to the count and writes it through a temporary file,
// so a crash leaves the previous count and never a half-written one.
func (a *app) countVisit() (int64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.visits + 1
	tmp := filepath.Join(a.dir, visitsFile+".tmp")
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(next, 10)+"\n"), 0o640); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, filepath.Join(a.dir, visitsFile)); err != nil {
		return 0, err
	}
	a.visits = next
	return next, nil
}

// health is 200 while the data volume accepts writes and 503 otherwise. It
// always says which revision this process was started as.
func (a *app) health(w http.ResponseWriter, _ *http.Request) {
	status, code := "ok", http.StatusOK
	probe := filepath.Join(a.dir, probeFile)
	switch {
	case a.cfg.Unhealthy:
		status, code = "unhealthy by configuration", http.StatusServiceUnavailable
	case os.WriteFile(probe, []byte("ok"), 0o640) != nil:
		status, code = "data volume not writable", http.StatusServiceUnavailable
	default:
		_ = os.Remove(probe) // a probe left behind is harmless; the next check rewrites it
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": status, "revision": a.cfg.Revision}) // a gone client is not an error here
}

// metrics serves the counters in the Prometheus text format.
func (a *app) metrics(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	visits := a.visits
	a.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = fmt.Fprintf(w, "# TYPE hello_up gauge\nhello_up 1\n"+
		"# TYPE hello_visits_total counter\nhello_visits_total %d\n"+
		"# TYPE hello_http_requests_total counter\nhello_http_requests_total %d\n", visits, a.requests.Load())
}
