//go:build e2e

package e2e

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	execdocker "github.com/bestagentkits/sisf/adapters/exec/docker"
	"github.com/bestagentkits/sisf/internal/dockerfilter"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

// renderedFlags returns the values of every --name=value argument of the
// command of a service in the install's rendered Compose file.
func renderedFlags(t *testing.T, d *composeDeployment, service, name string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(d.dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Services map[string]struct {
			Command []string `yaml:"command"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &f); err != nil {
		t.Fatalf("the rendered Compose file cannot be read: %v", err)
	}
	var values []string
	for _, a := range f.Services[service].Command {
		if v, ok := strings.CutPrefix(a, "--"+name+"="); ok {
			values = append(values, v)
		}
	}
	if len(values) == 0 {
		t.Fatalf("%s has no --%s in the rendered Compose file", service, name)
	}
	return values
}

// The runner image a Docker install builds and pins is the one a sandboxed
// build runs in. The test reads the pins the install rendered, puts a runner
// filter configured with them in front of the host's daemon, in process, and
// has the docker execution backend run the toolchain of the runner image on
// the install's own internal sandbox network. The runner container is
// removed when the command ends.
func TestComposeRunsASandboxedBuildInTheRunnerImageTheInstallPinned(t *testing.T) {
	d := newComposeRelease(t)
	socket := liveDockerSocket(t)
	runnerImage := renderedFlags(t, d, "supervisor", "runner-image")[0]
	network := renderedFlags(t, d, "supervisor", "exec-docker-network")[0]
	egress := renderedFlags(t, d, "supervisor", "egress-proxy")[0]
	factory := core.FactoryID(d.project)

	filter, err := dockerfilter.New(dockerfilter.Config{
		Upstream: dockerfilter.NewUnixTransport(socket), RunnerValue: factory, SandboxNetwork: network,
		Images: renderedFlags(t, d, "docker-filter-runner", "image"),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("the runner filter refuses the install's pins: %v", err)
	}
	front := httptest.NewServer(filter)
	t.Cleanup(front.Close)
	backend, err := execdocker.New(execdocker.Config{Endpoint: front.URL, FactoryID: factory, Network: network, EgressProxy: egress})
	if err != nil {
		t.Fatal(err)
	}

	sb, err := backend.CreateSandbox(t.Context(), execution.SandboxSpec{
		RunID: "run-e2e-runner-pin", Unit: "runner", Image: runnerImage, Trust: execution.TrustUntrusted,
		Egress: core.EgressPolicy{Mode: core.EgressAllowlist, Allow: []string{"proxy.golang.org"}},
		Limits: execution.Limits{Timeout: 5 * time.Minute}, ReadOnlyRootFS: true,
	})
	if err != nil {
		t.Fatalf("the backend refuses the runner image %q the install pinned: %v", runnerImage, err)
	}
	t.Cleanup(func() { _ = backend.Destroy(context.Background(), sb.ID) })
	res, err := backend.Exec(t.Context(), sb.ID, execution.Command{Argv: []string{"go", "version"}, Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatalf("the toolchain did not run in the runner image %q: %v", runnerImage, err)
	}
	if res.ExitCode != 0 || !strings.HasPrefix(string(res.Stdout), "go version go1.25") {
		t.Fatalf("go version in the runner: exit %d, stdout %q, stderr %q", res.ExitCode, res.Stdout, res.Stderr)
	}
}
