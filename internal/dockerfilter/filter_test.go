package dockerfilter_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/dockerfilter"
)

func TestARunnerGoesThroughItsWholeLifeThroughTheFilter(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	status, body := e.do(t, http.MethodGet, version+"/_ping", "")
	mustStatus(t, status, http.StatusOK, "ping", body)

	status, id := e.create(t, "sisf-runner-run-0001-1", validCreate())
	mustStatus(t, status, http.StatusCreated, "create", id)
	// The daemon receives the filter's own encoding: only known fields.
	created := e.daemon.Created()
	if len(created) != 1 {
		t.Fatalf("%d creates reached the daemon", len(created))
	}
	hostKeys := jsonKeys(t, jsonKeys(t, created[0].Body)["HostConfig"])
	for key := range hostKeys {
		switch key {
		case "NetworkMode", "ReadonlyRootfs", "Privileged", "CapAdd", "CapDrop", "SecurityOpt", "Runtime",
			"Memory", "MemorySwap", "NanoCpus", "PidsLimit", "Tmpfs":
		default:
			t.Fatalf("the forwarded body carries HostConfig.%s", key)
		}
	}

	// Actions may name the container by name; the daemon is addressed by ID.
	for _, step := range []struct{ method, path string }{
		{http.MethodPost, "/containers/sisf-runner-run-0001-1/start"},
		{http.MethodPost, "/containers/" + id + "/wait?condition=not-running"},
		{http.MethodGet, "/containers/" + id + "/logs?stdout=1&stderr=1"},
		{http.MethodGet, "/containers/" + id + "/json"},
		{http.MethodPost, "/containers/" + id + "/stop?t=0"},
		{http.MethodDelete, "/containers/" + id + "?force=1&v=1"},
	} {
		status, body := e.do(t, step.method, version+step.path, "")
		if status >= 300 {
			t.Fatalf("%s %s: status %d (%s)", step.method, step.path, status, body)
		}
	}
	if !e.reached(http.MethodPost, "/containers/"+id+"/start") {
		t.Fatal("the start was not sent to the container's full ID")
	}
	if len(e.daemon.Containers()) != 1 { // only control is left
		t.Fatalf("the runner was not removed: %+v", e.daemon.Containers())
	}
}

func TestInfoRevealsOnlyTheAllowedRuntimes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	status, body := e.do(t, http.MethodGet, version+"/info", "")
	mustStatus(t, status, http.StatusOK, "info", body)
	var info map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &info); err != nil {
		t.Fatal(err)
	}
	for key := range info {
		if key != "Runtimes" && key != "DefaultRuntime" {
			t.Fatalf("info leaks %s: %s", key, body)
		}
	}
	if !strings.Contains(body, "runsc") || strings.Contains(body, "nvidia") || strings.Contains(body, "/usr/bin") {
		t.Fatalf("info should name runc and runsc only: %s", body)
	}
}

func TestNoContainerButARunnerCanBeInspectedOrControlled(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for _, ref := range []string{e.control, e.control[:12], "acme-app-control"} {
		for _, step := range []struct{ method, suffix string }{
			{http.MethodGet, "/json"}, {http.MethodPost, "/start"}, {http.MethodPost, "/stop"},
			{http.MethodPost, "/wait"}, {http.MethodGet, "/logs?stdout=1"}, {http.MethodDelete, ""},
		} {
			status, body := e.do(t, step.method, version+"/containers/"+ref+step.suffix, "")
			mustStatus(t, status, http.StatusForbidden, step.method+" control"+step.suffix, body)
			if strings.Contains(body, "compose") {
				t.Fatalf("the refusal leaks the control container's details: %s", body)
			}
		}
	}
	for _, r := range e.daemon.Requests() {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.Path, "/json") {
			t.Fatalf("a request other than the filter's own inspect reached the daemon: %s %s", r.Method, r.Path)
		}
	}
	if len(e.daemon.Containers()) != 1 {
		t.Fatal("the control container was removed")
	}
}

func TestEndpointsTheAdapterDoesNotNeedAreRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, runner := e.create(t, "sisf-runner-x", validCreate())
	refused := []struct{ method, path string }{
		{http.MethodPost, "/containers/" + runner + "/exec"},
		{http.MethodPost, "/exec/abc/start"},
		{http.MethodPost, "/containers/" + runner + "/attach?stream=1"},
		{http.MethodGet, "/containers/" + runner + "/attach/ws"},
		{http.MethodGet, "/containers/" + runner + "/archive?path=/"},
		{http.MethodPut, "/containers/" + runner + "/archive?path=/"},
		{http.MethodGet, "/containers/" + runner + "/export"},
		{http.MethodPost, "/containers/" + runner + "/update"},
		{http.MethodPost, "/containers/" + runner + "/rename?name=egress"},
		{http.MethodPost, "/containers/" + runner + "/kill"},
		{http.MethodGet, "/containers/" + runner + "/top"},
		{http.MethodGet, "/containers/json"},
		{http.MethodPost, "/containers/prune"},
		{http.MethodPost, "/commit?container=" + runner},
		{http.MethodPost, "/images/create?fromImage=alpine"},
		{http.MethodPost, "/images/load"},
		{http.MethodGet, "/images/json"},
		{http.MethodPost, "/build"},
		{http.MethodGet, "/volumes"},
		{http.MethodPost, "/volumes/create"},
		{http.MethodGet, "/networks"},
		{http.MethodPost, "/networks/create"},
		{http.MethodPost, "/networks/" + network + "/connect"},
		{http.MethodGet, "/swarm"},
		{http.MethodGet, "/services"},
		{http.MethodGet, "/secrets"},
		{http.MethodGet, "/configs"},
		{http.MethodGet, "/plugins"},
		{http.MethodGet, "/system/df"},
		{http.MethodGet, "/events"},
		{http.MethodGet, "/version"},
		{http.MethodPost, "/auth"},
		{http.MethodPost, "/session"},
		{http.MethodPost, "/info"},
		{http.MethodDelete, "/containers/create"},
	}
	before := len(e.daemon.Requests())
	for _, r := range refused {
		status, body := e.do(t, r.method, version+r.path, "")
		if status != http.StatusForbidden {
			t.Errorf("%s %s: status %d, want 403 (%s)", r.method, r.path, status, body)
		}
	}
	if got := e.daemon.Requests()[before:]; len(got) != 0 {
		t.Fatalf("refused requests reached the daemon: %+v", got)
	}
}

func TestUnknownAPIVersionsAndUnversionedPathsAreRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for _, path := range []string{"/_ping", "/v1.12/_ping", "/v1.99/_ping", "/v2.0/_ping", "/v1.45.1/_ping", "/containers/create", "/vx/_ping"} {
		status, body := e.do(t, http.MethodGet, path, "")
		if status != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403 (%s)", path, status, body)
		}
	}
	if n := len(e.daemon.Requests()); n != 0 {
		t.Fatalf("%d refused requests reached the daemon", n)
	}
}

func TestTheFilterRefusesAnUnsafeConfiguration(t *testing.T) {
	t.Parallel()
	good := dockerfilter.Config{Upstream: testClient.Transport, RunnerValue: factory, SandboxNetwork: network, Images: []string{image}}
	if _, err := dockerfilter.New(good); err != nil {
		t.Fatalf("a good configuration was refused: %v", err)
	}
	for name, mutate := range map[string]func(*dockerfilter.Config){
		"no transport":         func(c *dockerfilter.Config) { c.Upstream = nil },
		"no factory":           func(c *dockerfilter.Config) { c.RunnerValue = "" },
		"host network":         func(c *dockerfilter.Config) { c.SandboxNetwork = "host" },
		"bridge network":       func(c *dockerfilter.Config) { c.SandboxNetwork = "bridge" },
		"no images":            func(c *dockerfilter.Config) { c.Images = nil },
		"image by tag":         func(c *dockerfilter.Config) { c.Images = []string{"ghcr.io/acme/runner:latest"} },
		"label outside sisf":   func(c *dockerfilter.Config) { c.RunnerLabel = "traefik.enable" },
		"bad API version":      func(c *dockerfilter.Config) { c.APIVersions = []string{"latest"} },
		"upstream with a path": func(c *dockerfilter.Config) { c.UpstreamURL = "http://docker/v1.45" },
		"name prefix no dash":  func(c *dockerfilter.Config) { c.NamePrefix = "runner" },
	} {
		cfg := good
		mutate(&cfg)
		if _, err := dockerfilter.New(cfg); err == nil {
			t.Errorf("%s: the configuration was accepted", name)
		}
	}
}
