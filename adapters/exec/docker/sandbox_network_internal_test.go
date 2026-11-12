package docker_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/adapters/exec/docker"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

// A sandbox network that is not internal lets a runner reach the internet
// around the egress proxy, so the backend declares nothing and creates no
// sandbox, trusted or not, until the network is internal.
func TestNoSandboxIsCreatedOnANetworkThatIsNotInternal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, options{externalNetwork: true})
	_, err := e.backend.Capabilities(ctx)
	wantCode(t, err, core.CodeNotSupported, "capabilities on a non-internal network")
	if err == nil || !strings.Contains(err.Error(), "not internal") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
	for _, trust := range []execution.Trust{execution.TrustUntrusted, execution.TrustTrusted} {
		_, err := e.backend.CreateSandbox(ctx, spec("run-0001", trust))
		wantCode(t, err, core.CodeNotSupported, "a "+string(trust)+" sandbox on a non-internal network")
	}
	if created := e.daemon.Created(); len(created) != 0 {
		t.Fatalf("a container was created on a non-internal network: %d", len(created))
	}

	// The network is read again each time: once it is internal, sandboxes run.
	e.daemon.AddNetwork(network, true)
	if _, err := e.backend.Capabilities(ctx); err != nil {
		t.Fatalf("capabilities on an internal network: %v", err)
	}
	e.create(t, spec("run-0002", execution.TrustUntrusted))
}

func TestNoSandboxIsCreatedWhenTheSandboxNetworkIsMissing(t *testing.T) {
	t.Parallel()
	e := newEnv(t, options{noNetwork: true})
	_, err := e.backend.CreateSandbox(context.Background(), spec("run-0001", execution.TrustUntrusted))
	wantCode(t, err, core.CodeNotSupported, "a sandbox on a missing network")
}

// A backend pointed at another network than its filter's is refused by the
// filter, and creates nothing.
func TestABackendOnAnotherNetworkThanItsFiltersCreatesNothing(t *testing.T) {
	t.Parallel()
	e := newEnv(t, options{cfg: func(c *docker.Config) { c.Network = "acme-app_other" }})
	if _, err := e.backend.CreateSandbox(context.Background(), spec("run-0001", execution.TrustUntrusted)); err == nil {
		t.Fatal("a sandbox was created on a network the filter does not serve")
	}
	if created := e.daemon.Created(); len(created) != 0 {
		t.Fatalf("a container was created: %d", len(created))
	}
}
