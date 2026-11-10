package dockerfilter_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/dockerfilter"
	"github.com/bestagentkits/sisf/internal/dockerfilter/dockerfiltertest"
)

// The runner filter lets its client read whether the sandbox network is
// internal, and nothing more: no other network, no other method, no query,
// and none of what the daemon says about the containers on it.
func TestTheRunnerFilterAnswersOnlyWhetherItsSandboxNetworkIsInternal(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.daemon.AddNetwork(network, true)
	e.daemon.AddNetwork("acme-app_app", false)

	status, body := e.do(t, http.MethodGet, version+"/networks/"+network, "")
	var view map[string]any
	if status != http.StatusOK || json.Unmarshal([]byte(body), &view) != nil {
		t.Fatalf("the sandbox network: %d %s", status, body)
	}
	if len(view) != 2 || view["Name"] != network || view["Internal"] != true {
		t.Fatalf("the filter answered more or other than the name and the internal flag: %s", body)
	}

	before := len(e.daemon.Requests())
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/networks/acme-app_app"},
		{http.MethodGet, "/networks/bridge"},
		{http.MethodGet, "/networks/" + network + "x"},
		{http.MethodGet, "/networks/" + network[:4]},
		{http.MethodDelete, "/networks/" + network},
		{http.MethodPost, "/networks/" + network},
		{http.MethodGet, "/networks/" + network + "/connect"},
		{http.MethodGet, "/networks/" + network + "?verbose=true"},
		{http.MethodGet, "/networks/" + network + "?scope=swarm"},
	} {
		if status, body := e.do(t, r.method, version+r.path, ""); status != http.StatusForbidden {
			t.Errorf("%s %s: %d, want 403 (%s)", r.method, r.path, status, body)
		}
	}
	if got := e.daemon.Requests()[before:]; len(got) != 0 {
		t.Fatalf("refused network requests reached the daemon: %+v", got)
	}
}

// A sandbox network the daemon does not have is NOT_FOUND, and an answer
// about another network, which the daemon gives for an ID prefix, is not
// passed on as the sandbox network's.
func TestTheSandboxNetworkAnswerIsTheSandboxNetworksOwn(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if status, body := e.do(t, http.MethodGet, version+"/networks/"+network, ""); status != http.StatusNotFound {
		t.Fatalf("a missing sandbox network: %d %s", status, body)
	}

	daemon := dockerfiltertest.New("runc")
	other := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/networks/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"Name":"someone-else","Internal":true}`)
			return
		}
		daemon.ServeHTTP(w, r)
	})
	upstream := httptest.NewServer(other)
	t.Cleanup(upstream.Close)
	f, err := dockerfilter.New(dockerfilter.Config{Upstream: upstream.Client().Transport, UpstreamURL: upstream.URL,
		RunnerValue: factory, SandboxNetwork: network, Images: []string{image}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(f)
	t.Cleanup(front.Close)
	resp, err := http.Get(front.URL + version + "/networks/" + network)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("an answer about another network: %d, want 502", resp.StatusCode)
	}
}
