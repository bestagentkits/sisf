package install

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	execdocker "github.com/bestagentkits/sisf/adapters/exec/docker"
	"github.com/bestagentkits/sisf/internal/dockerfilter"
	"github.com/bestagentkits/sisf/internal/dockerfilter/dockerfiltertest"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

// commandFlag returns the value of the one --name=value argument of a
// rendered service command, failing when it is missing or repeated.
func commandFlag(t *testing.T, s composeService, name string) []string {
	t.Helper()
	var argv []string
	if err := s.Command.Decode(&argv); err != nil {
		t.Fatalf("the command is not a list: %v", err)
	}
	var values []string
	for _, a := range argv {
		if v, ok := strings.CutPrefix(a, "--"+name+"="); ok {
			values = append(values, v)
		}
	}
	if len(values) == 0 {
		t.Fatalf("the command has no --%s: %q", name, argv)
	}
	return values
}

// A Docker install builds its runner image locally, where Docker records no
// registry digest, so the install pins it by its image ID. The supervisor
// hands that pin to the docker execution backend, which must run a build in
// it through the runner filter the install configures with the same pins.
func TestTheRunnerImagePinnedByADockerInstallRunsASandboxedBuild(t *testing.T) {
	t.Parallel()
	in, _ := upDocker(t)
	rendered, err := os.ReadFile(in.Layout.Compose())
	if err != nil {
		t.Fatal(err)
	}
	f := composeFileOf(t, rendered)
	supervisor, runnerFilter := f.Services["supervisor"], f.Services["docker-filter-runner"]
	runnerImage := commandFlag(t, supervisor, "runner-image")[0]
	network := commandFlag(t, supervisor, "exec-docker-network")[0]
	factory := core.FactoryID(commandFlag(t, supervisor, "factory-id")[0])
	if got := commandFlag(t, runnerFilter, "network")[0]; got != network {
		t.Fatalf("the runner filter guards network %q, the supervisor builds on %q", got, network)
	}

	daemon := dockerfiltertest.New("runc")
	daemon.AddNetwork(network, true)
	upstream := httptest.NewServer(daemon)
	t.Cleanup(upstream.Close)
	filter, err := dockerfilter.New(dockerfilter.Config{
		Upstream: upstream.Client().Transport, UpstreamURL: upstream.URL, RunnerValue: factory,
		SandboxNetwork: network, Images: commandFlag(t, runnerFilter, "image"),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("the runner filter refuses the install's settings: %v", err)
	}
	front := httptest.NewServer(filter)
	t.Cleanup(front.Close)
	backend, err := execdocker.New(execdocker.Config{Endpoint: front.URL, FactoryID: factory, Network: network, EgressProxy: "http://egress:3128"})
	if err != nil {
		t.Fatal(err)
	}

	sb, err := backend.CreateSandbox(t.Context(), execution.SandboxSpec{
		RunID: "run-0001", Unit: "runner", Image: runnerImage, Trust: execution.TrustUntrusted,
		Egress: core.EgressPolicy{Mode: core.EgressAllowlist, Allow: []string{"proxy.golang.org"}},
		Limits: execution.Limits{Timeout: 10 * time.Minute}, ReadOnlyRootFS: true,
	})
	if err != nil {
		t.Fatalf("the backend refuses the runner image %q the install pinned: %v", runnerImage, err)
	}
	res, err := backend.Exec(t.Context(), sb.ID, execution.Command{Argv: []string{"go", "version"}})
	if err != nil {
		t.Fatalf("the build command did not run in the runner image %q: %v", runnerImage, err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("the build command exited %d", res.ExitCode)
	}
	created := daemon.Created()
	if len(created) != 1 || !strings.Contains(string(created[0].Body), `"Image":"`+runnerImage+`"`) {
		t.Fatalf("the daemon was not asked for a runner from %q: %d creates", runnerImage, len(created))
	}
}
