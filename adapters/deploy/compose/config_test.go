package compose_test

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/adapters/deploy/compose"
	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/core"
)

func validConfig(ctl edge.Controller) compose.Config {
	return compose.Config{Endpoint: "http://127.0.0.1:2375", Factory: factory, Network: "acme-app_default", AppPort: 7461, Edge: ctl}
}

func TestTheBackendNeverFindsADockerEndpointOnItsOwn(t *testing.T) {
	// Not parallel: it sets the environment.
	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	cfg := validConfig(brokenEdge{})
	cfg.Endpoint = ""
	if _, err := compose.New(cfg); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("New without an endpoint: %v, want INVALID_ARGUMENT even with DOCKER_HOST set", err)
	}
}

func TestMalformedConfigurationIsRefused(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*compose.Config){
		"endpoint with a path":       func(c *compose.Config) { c.Endpoint = "http://127.0.0.1:2375/x" },
		"endpoint with credentials":  func(c *compose.Config) { c.Endpoint = "http://u:p@127.0.0.1:2375" },
		"endpoint by tcp":            func(c *compose.Config) { c.Endpoint = "tcp://127.0.0.1:2375" },
		"relative socket":            func(c *compose.Config) { c.Endpoint = "unix://socket" },
		"no factory":                 func(c *compose.Config) { c.Factory = "" },
		"no network":                 func(c *compose.Config) { c.Network = "" },
		"no port":                    func(c *compose.Config) { c.AppPort = 0 },
		"no edge":                    func(c *compose.Config) { c.Edge = nil },
		"environment sets revision":  func(c *compose.Config) { c.Env = []string{"SISF_REVISION=sha256:x"} },
		"environment sets docker":    func(c *compose.Config) { c.Env = []string{"DOCKER_HOST=unix:///x"} },
		"environment without value":  func(c *compose.Config) { c.Env = []string{"JUSTANAME"} },
		"volume mounted at the root": func(c *compose.Config) { c.Volumes = []compose.VolumeMount{{Name: "data", Target: "/"}} },
		"volume with a dot segment":  func(c *compose.Config) { c.Volumes = []compose.VolumeMount{{Name: "data", Target: "/a/../etc"}} },
		"a negative memory limit":    func(c *compose.Config) { c.Limits.MemoryBytes = -1 },
		"a negative process limit":   func(c *compose.Config) { c.Limits.Pids = -1 },
	} {
		cfg := validConfig(brokenEdge{})
		mutate(&cfg)
		if _, err := compose.New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := compose.New(validConfig(brokenEdge{})); err != nil {
		t.Fatalf("a valid configuration was refused: %v", err)
	}
}

func TestTheBackendWorksThroughAUnixSocketItWasHanded(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "sk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	w := newWorld(t, worldOptions{cfg: func(c *compose.Config) { c.Endpoint = "unix://" + socket }})
	srv := &http.Server{Handler: http.HandlerFunc(w.serveDocker)}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	w.stage(digestA)
	if !w.running(name(digestA)) || !strings.HasPrefix(w.daemon.Created()[0].Path, "/v1.43/") {
		t.Fatalf("the unix endpoint was not used: %+v", w.daemon.Requests())
	}
}
