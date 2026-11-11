package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const revision = "sha256:0123"

// instance answers like a healthy sample app of revision.
func instance(t *testing.T) *httptest.Server {
	t.Helper()
	var visits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case r.URL.Path == "/healthz":
			_, _ = fmt.Fprintf(w, `{"status":"ok","revision":%q}`, revision)
		case r.URL.Path == "/metrics":
			_, _ = fmt.Fprintln(w, "hello_up 1")
		default:
			_, _ = fmt.Fprintf(w, "hello, visit %d", visits.Add(1))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The program passes an instance of the expected revision whose every flow
// works, fails one that reports another revision, and refuses a command
// line without both the URL and the revision.
func TestTheProgramExitsWithTheSuitesVerdict(t *testing.T) {
	t.Parallel()
	srv := instance(t)
	for _, tc := range []struct {
		name string
		args []string
		code int
		out  string
	}{
		{"healthy", []string{"--url", srv.URL, "--revision", revision}, 0, "ok   health reports the expected revision"},
		{"another revision", []string{"--url", srv.URL, "--revision", "sha256:9999"}, 1, "FAIL health reports the expected revision"},
		{"no revision", []string{"--url", srv.URL}, 2, ""},
		{"stray argument", []string{"--url", srv.URL, "--revision", revision, "x"}, 2, ""},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(context.Background(), tc.args, &stdout, &stderr); got != tc.code || !strings.Contains(stdout.String(), tc.out) {
			t.Errorf("%s: exit %d, want %d\n%s%s", tc.name, got, tc.code, stdout.String(), stderr.String())
		}
	}
}
