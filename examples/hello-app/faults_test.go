package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/examples/hello-app/synthetic"
)

const testRevision = "sha256:" + "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

func newFaultyApp(t *testing.T, s settings, opts ...option) *app {
	t.Helper()
	a, err := newApp(t.TempDir(), append([]option{withSettings(s)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestHealthzReportsTheRevisionTheProcessWasStartedAs(t *testing.T) {
	t.Parallel()
	a := newFaultyApp(t, settings{Revision: testRevision})
	code, body := serve(t, a.handler(), http.MethodGet, "/healthz")
	var got struct{ Status, Revision string }
	if err := json.Unmarshal([]byte(body), &got); err != nil || code != http.StatusOK || got.Revision != testRevision || got.Status != "ok" {
		t.Fatalf("GET /healthz = %d %q (%v), want 200 ok with the revision", code, body, err)
	}
}

func TestAnInjectedErrorShareFailsEvenlySpreadPageRequests(t *testing.T) {
	t.Parallel()
	a := newFaultyApp(t, settings{ErrorPercent: 25})
	h := a.handler()
	failures := 0
	for range 100 {
		if code, _ := serve(t, h, http.MethodGet, "/"); code == http.StatusInternalServerError {
			failures++
		}
	}
	if failures != 25 {
		t.Fatalf("%d of 100 requests failed, want 25", failures)
	}
	// Health and metrics are not page requests and are never failed this way.
	if code, _ := serve(t, h, http.MethodGet, "/healthz"); code != http.StatusOK {
		t.Fatalf("GET /healthz = %d under injected page errors", code)
	}
}

func TestAnInjectedFailureDoesNotCountAVisit(t *testing.T) {
	t.Parallel()
	a := newFaultyApp(t, settings{ErrorPercent: 100})
	serve(t, a.handler(), http.MethodGet, "/")
	if _, body := serve(t, a.handler(), http.MethodGet, "/metrics"); !strings.Contains(body, "hello_visits_total 0") {
		t.Fatalf("a failed request counted as a visit:\n%s", body)
	}
}

func TestAnUnhealthyInstanceAnswers503ButStillNamesItsRevision(t *testing.T) {
	t.Parallel()
	a := newFaultyApp(t, settings{Unhealthy: true, Revision: testRevision})
	code, body := serve(t, a.handler(), http.MethodGet, "/healthz")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, testRevision) {
		t.Fatalf("GET /healthz = %d %q, want 503 naming the revision", code, body)
	}
}

func TestACriticalLineIsLoggedOnlyWhenAskedFor(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	quiet := newFaultyApp(t, settings{ErrorPercent: 100}, withLogs(&logs))
	serve(t, quiet.handler(), http.MethodGet, "/")
	if logs.Len() != 0 {
		t.Fatalf("a critical line was logged without being asked: %q", logs.String())
	}
	loud := newFaultyApp(t, settings{ErrorPercent: 100, CriticalLog: true, Revision: testRevision}, withLogs(&logs))
	serve(t, loud.handler(), http.MethodGet, "/")
	if !strings.Contains(logs.String(), "level=critical") || !strings.Contains(logs.String(), testRevision) {
		t.Fatalf("log = %q, want a critical line naming the revision", logs.String())
	}
}

func TestSettingsComeFromTheEnvironmentAndMalformedOnesAreRefused(t *testing.T) {
	t.Parallel()
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	got, err := settingsFromEnv(env(map[string]string{envRevision: "r1", envErrorPct: "40", envUnhealthy: "true", envCriticalLog: "1"}))
	if err != nil || got != (settings{Revision: "r1", ErrorPercent: 40, Unhealthy: true, CriticalLog: true}) {
		t.Fatalf("settings = %+v, %v", got, err)
	}
	if got, err := settingsFromEnv(env(nil)); err != nil || got != (settings{}) {
		t.Fatalf("empty environment: %+v, %v, want everything off", got, err)
	}
	for name, m := range map[string]map[string]string{
		"percent not a number": {envErrorPct: "many"},
		"percent over 100":     {envErrorPct: "101"},
		"negative percent":     {envErrorPct: "-1"},
		"bad bool":             {envUnhealthy: "maybe"},
		"revision too long":    {envRevision: strings.Repeat("r", 201)},
	} {
		if _, err := settingsFromEnv(env(m)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRunRefusesAMalformedFaultSetting(t *testing.T) {
	t.Parallel()
	getenv := func(k string) string {
		return map[string]string{envErrorPct: "lots", "HELLO_DATA_DIR": t.TempDir()}[k]
	}
	if err := run(context.Background(), getenv); err == nil {
		t.Fatal("the app started with a malformed fault setting")
	}
}

func TestTheSyntheticSuiteJudgesTheRealApp(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		cfg  settings
		pass bool
	}{
		"healthy":         {settings{Revision: testRevision}, true},
		"page errors":     {settings{Revision: testRevision, ErrorPercent: 100}, false},
		"unhealthy":       {settings{Revision: testRevision, Unhealthy: true}, false},
		"another version": {settings{Revision: "sha256:other"}, false},
	} {
		srv := httptest.NewServer(newFaultyApp(t, tc.cfg).handler())
		report := synthetic.Run(context.Background(), synthetic.Target{BaseURL: srv.URL, ExpectedRevision: testRevision})
		srv.Close()
		if report.Passed() != tc.pass {
			t.Errorf("%s: passed = %v, failures %+v", name, report.Passed(), report.Failures())
		}
	}
}
