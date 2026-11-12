package healthprobe_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy/healthprobe"
)

func serve(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("probed %s, want /healthz", r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestProbeReadsHealthAndRevision(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		status int
		body   string
		want   healthprobe.Health
	}{
		"healthy":             {200, `{"status":"ok","revision":"r1"}`, healthprobe.Health{Healthy: true, Revision: "r1"}},
		"unhealthy but named": {503, `{"status":"down","revision":"r1"}`, healthprobe.Health{Revision: "r1"}},
		"200 with bad status": {200, `{"status":"degraded","revision":"r1"}`, healthprobe.Health{Revision: "r1"}},
		"no revision":         {200, `{"status":"ok"}`, healthprobe.Health{Healthy: true}},
	} {
		got, err := healthprobe.Probe(context.Background(), testClient, serve(t, tc.status, tc.body))
		if err != nil || got != tc.want {
			t.Errorf("%s: %+v, %v, want %+v", name, got, err, tc.want)
		}
	}
}

func TestProbeRefusesWhatItCannotReadOrReach(t *testing.T) {
	t.Parallel()
	for name, url := range map[string]string{
		"not json":    serve(t, 200, "OK"),
		"empty":       serve(t, 200, ""),
		"unreachable": "http://127.0.0.1:1",
	} {
		if _, err := healthprobe.Probe(context.Background(), testClient, url); !core.HasCode(err, core.CodeUnavailable) {
			t.Errorf("%s: err = %v, want UNAVAILABLE", name, err)
		}
	}
	if _, err := healthprobe.Probe(context.Background(), testClient, "http://bad host"); err == nil || strings.Contains(err.Error(), "ok") {
		t.Errorf("a malformed address was probed: %v", err)
	}
}

func TestCriticalLinesAreRecognisedInTextAndJSONForm(t *testing.T) {
	t.Parallel()
	for line, want := range map[string]bool{
		"level=critical msg=boom":         true,
		`{"level":"FATAL","msg":"x"}`:     true,
		`{"level": "critical"}`:           true,
		"level=error msg=minor":           false,
		"level=info msg=critical-path ok": false,
		"the word critical alone":         false,
		`{"level":"info","msg":"fatal"}`:  false,
	} {
		if got := healthprobe.IsCriticalLine(line); got != want {
			t.Errorf("IsCriticalLine(%q) = %v, want %v", line, got, want)
		}
	}
}
