//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/dockerfilter"
)

// The live filter test puts the Docker request filter in front of the
// host's real daemon, in process, and checks that what a compromised
// runner manager would ask for is refused before the daemon sees it. It
// starts no container: the only create it sends is one the filter refuses.

const (
	liveFilterFactory = "sisf-e2e-filter"
	liveFilterNetwork = "sisf-e2e-sandbox"
	liveFilterImage   = "ghcr.io/acme/sisf-runner@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	liveFilterVersion = "/v1.44"
)

// liveDockerSocket returns the path of the daemon's unix socket, skipping
// the test when the daemon is reached some other way or not at all.
func liveDockerSocket(t *testing.T) string {
	t.Helper()
	path := "/var/run/docker.sock"
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		u, err := url.Parse(host)
		if err != nil || u.Scheme != "unix" {
			t.Skipf("DOCKER_HOST %q is not a unix socket", host)
		}
		path = u.Path
	}
	if info, err := os.Stat(path); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Skipf("no Docker socket at %s", path)
	}
	return path
}

// liveDockerRaw asks the daemon directly, without the filter, for the
// test's own bookkeeping.
func liveDockerRaw(t *testing.T, socket, path string, v any) {
	t.Helper()
	client := &http.Client{Transport: dockerfilter.NewUnixTransport(socket)}
	resp, err := client.Get("http://docker" + liveFilterVersion + path)
	if err != nil {
		t.Skipf("the Docker daemon does not answer: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s", path, resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(v); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
}

// liveFilterDo sends one request through the filter.
func liveFilterDo(t *testing.T, f *dockerfilter.Filter, method, path string, body []byte) (int, string) {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, liveFilterVersion+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

type liveContainer struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Labels map[string]string `json:"Labels"`
}

func TestTheDockerFilterRefusesControlContainersAndEscapingRunnersOnTheLiveDaemon(t *testing.T) {
	socket := liveDockerSocket(t)
	f, err := dockerfilter.New(dockerfilter.Config{
		Upstream: dockerfilter.NewUnixTransport(socket), RunnerValue: liveFilterFactory,
		SandboxNetwork: liveFilterNetwork, Images: []string{liveFilterImage},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	if code, _ := liveFilterDo(t, f, http.MethodGet, "/_ping", nil); code != http.StatusOK {
		t.Fatalf("the filter does not reach the daemon: %d", code)
	}

	// Any container that is not this factory's runner, such as the factory's
	// own control-plane units, cannot be inspected, stopped or removed.
	var running []liveContainer
	liveDockerRaw(t, socket, "/containers/json", &running)
	var other *liveContainer
	for i := range running {
		if running[i].Labels[dockerfilter.DefaultRunnerLabel] != liveFilterFactory {
			other = &running[i]
			break
		}
	}
	if other == nil {
		t.Log("no other container is running; the inspect refusal is not exercised")
	} else {
		for _, probe := range []struct{ method, path string }{
			{http.MethodGet, "/containers/" + other.ID + "/json"},
			{http.MethodPost, "/containers/" + other.ID + "/stop"},
			{http.MethodDelete, "/containers/" + other.ID},
		} {
			if code, body := liveFilterDo(t, f, probe.method, probe.path, nil); code != http.StatusForbidden {
				t.Errorf("%s %s of %v: %d %s", probe.method, probe.path, other.Names, code, body)
			}
		}
	}

	// A runner that asks for privilege, the host's network or PID namespace,
	// another network or a bind of the host's root or of the Docker socket
	// is refused and never created.
	escapes := map[string]func(host map[string]any, body map[string]any){
		"privileged":    func(h, _ map[string]any) { h["Privileged"] = true },
		"host network":  func(h, _ map[string]any) { h["NetworkMode"] = "host" },
		"other network": func(h, _ map[string]any) { h["NetworkMode"] = "bridge" },
		"host pid":      func(h, _ map[string]any) { h["PidMode"] = "host" },
		"bind of root":  func(h, _ map[string]any) { h["Binds"] = []string{"/:/host"} },
		"docker socket": func(h, _ map[string]any) { h["Binds"] = []string{"/var/run/docker.sock:/var/run/docker.sock"} },
		"second network": func(_, b map[string]any) {
			b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{liveFilterNetwork: map[string]any{}, "bridge": map[string]any{}}}
		},
	}
	var names []string
	for escape, mutate := range escapes {
		name := "sisf-runner-e2e-" + strings.ReplaceAll(escape, " ", "-")
		names = append(names, name)
		host := map[string]any{
			"NetworkMode": liveFilterNetwork, "ReadonlyRootfs": true,
			"CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges"},
			"Memory": 256 << 20, "MemorySwap": 256 << 20, "NanoCpus": 500_000_000, "PidsLimit": 64,
		}
		create := map[string]any{
			"Image": liveFilterImage, "Cmd": []string{"/bin/true"},
			"Labels": map[string]string{dockerfilter.DefaultRunnerLabel: liveFilterFactory}, "User": "65534:65534",
			"HostConfig": host,
		}
		mutate(host, create)
		raw, err := json.Marshal(create)
		if err != nil {
			t.Fatal(err)
		}
		if code, body := liveFilterDo(t, f, http.MethodPost, "/containers/create?name="+name, raw); code != http.StatusForbidden {
			t.Errorf("a create with %s: %d %s", escape, code, body)
		}
	}

	// Moving a container to another network or changing its limits is
	// refused whatever container it names.
	target := "sisf-runner-e2e-absent"
	if other != nil {
		target = other.ID
	}
	for _, probe := range []struct{ path, body string }{
		{"/networks/bridge/connect", `{"Container":"` + target + `","EndpointConfig":{}}`},
		{"/networks/" + liveFilterNetwork + "/connect", `{"Container":"` + target + `","EndpointConfig":{"Aliases":["egress"]}}`},
		{"/networks/bridge/disconnect", `{"Container":"` + target + `","Force":true}`},
		{"/containers/" + target + "/update", `{"Memory":0,"PidsLimit":-1,"RestartPolicy":{"Name":"always"}}`},
	} {
		if code, body := liveFilterDo(t, f, http.MethodPost, probe.path, []byte(probe.body)); code != http.StatusForbidden {
			t.Errorf("POST %s: %d %s", probe.path, code, body)
		}
	}

	var all []liveContainer
	liveDockerRaw(t, socket, "/containers/json?all=1", &all)
	for _, c := range all {
		for _, n := range c.Names {
			if slices.Contains(names, strings.TrimPrefix(n, "/")) {
				t.Fatalf("the refused runner %s exists on the daemon", n)
			}
		}
	}
}
