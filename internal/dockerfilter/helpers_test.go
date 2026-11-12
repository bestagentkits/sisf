package dockerfilter_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/dockerfilter"
	"github.com/bestagentkits/sisf/internal/dockerfilter/dockerfiltertest"
)

const (
	factory = "acme-app"
	network = "acme-app_sandbox"
	version = "/v1.45"
)

var image = "ghcr.io/acme/sisf-runner@sha256:" + strings.Repeat("a", 64)

// env is a filter in front of a fake daemon that also holds the factory's
// control container.
type env struct {
	daemon  *dockerfiltertest.Daemon
	filter  *httptest.Server
	control string
}

func newEnv(t *testing.T) env {
	t.Helper()
	daemon := dockerfiltertest.New("runc", "runsc", "nvidia")
	upstream := httptest.NewServer(daemon)
	t.Cleanup(upstream.Close)
	f, err := dockerfilter.New(dockerfilter.Config{
		Upstream: upstream.Client().Transport, UpstreamURL: upstream.URL,
		RunnerValue: factory, SandboxNetwork: network, Images: []string{image},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	control := daemon.AddContainer("acme-app-control", map[string]string{"com.docker.compose.service": "control"})
	return env{daemon: daemon, filter: srv, control: control}
}

// validCreate is a create body the filter accepts.
func validCreate() map[string]any {
	return map[string]any{
		"Image":      image,
		"Cmd":        []string{"/bin/agent", "--run", "run-0001"},
		"Env":        []string{"HTTPS_PROXY=http://edge:3128"},
		"Labels":     map[string]any{"sisf.runner": factory, "sisf.run_id": "run-0001"},
		"WorkingDir": "/workspace",
		"User":       "65534:65534",
		"HostConfig": map[string]any{
			"NetworkMode": network, "ReadonlyRootfs": true, "Privileged": false,
			"CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges"}, "Runtime": "runsc",
			"Memory": 1 << 30, "MemorySwap": 1 << 30, "NanoCpus": 1_000_000_000, "PidsLimit": 256,
			"Tmpfs": map[string]any{"/workspace": "rw,exec,nosuid,nodev,size=1g", "/tmp": "rw,nosuid,size=256m"},
		},
		"NetworkingConfig": map[string]any{"EndpointsConfig": map[string]any{network: map[string]any{}}},
	}
}

func host(body map[string]any) map[string]any { return body["HostConfig"].(map[string]any) }

func encode(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// do sends a request through a normal client and returns the status and body.
func (e env) do(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.filter.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

// raw writes a request byte for byte, so a test controls the request line,
// the framing and the encoding of the path.
func (e env) raw(t *testing.T, request string) (int, string) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(e.filter.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

// create asks for a runner container and returns the status and its ID.
func (e env) create(t *testing.T, name string, body map[string]any) (int, string) {
	t.Helper()
	status, out := e.do(t, http.MethodPost, version+"/containers/create?name="+name, encode(t, body))
	var created struct {
		ID string `json:"Id"`
	}
	_ = json.Unmarshal([]byte(out), &created)
	return status, created.ID
}

// reached reports whether any request that reached the daemon matches.
func (e env) reached(method, pathPart string) bool {
	for _, r := range e.daemon.Requests() {
		if r.Method == method && strings.Contains(r.Path, pathPart) {
			return true
		}
	}
	return false
}

func mustStatus(t *testing.T, got, want int, what, body string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: status %d, want %d (%s)", what, got, want, strings.TrimSpace(body))
	}
}

func jsonKeys(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}
