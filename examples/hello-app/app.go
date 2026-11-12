package main

import (
	"errors"
	"fmt"
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
	mu       sync.Mutex // serializes the read-increment-write of the count
	visits   int64
	requests atomic.Int64
}

// newApp opens the data volume and loads the stored visit count.
func newApp(dir string) (*app, error) {
	if dir == "" {
		return nil, errors.New("the data directory is not set")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("data directory: %w", err)
	}
	a := &app{dir: dir}
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

// health is 200 while the data volume accepts writes and 503 otherwise.
func (a *app) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	probe := filepath.Join(a.dir, probeFile)
	if err := os.WriteFile(probe, []byte("ok"), 0o640); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"data volume not writable"}` + "\n"))
		return
	}
	_ = os.Remove(probe) // a probe left behind is harmless; the next check rewrites it
	_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
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
