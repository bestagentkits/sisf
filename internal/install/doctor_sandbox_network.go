package install

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// sandboxNetworkCheck names the doctor's check of the sandbox network.
const sandboxNetworkCheck = "sandbox network"

// checkSandboxNetwork proves the network candidate builds run on is
// internal. Its only way out must be the egress proxy; on an ordinary
// bridge a runner reaches the internet around it, and the docker execution
// backend refuses to build there. Docker mode reads the Compose project's
// sandbox network; a Standalone install reads its build sandbox's network
// through the runner filter, as the backend does, and has nothing to check
// without one.
func (in *Installer) checkSandboxNetwork(ctx context.Context, c *collector, m *manifest.Manifest) {
	if m.Mode == manifest.ModeDocker {
		name := project(m.FactoryID) + "-sandbox"
		out, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: []string{"network", "inspect", "--format", "{{.Internal}}", name}})
		if err != nil {
			c.fail(sandboxNetworkCheck, "the sandbox network %s does not exist: %s", name, startHint)
			return
		}
		reportSandboxNetwork(c, name, strings.TrimSpace(string(out)) == "true")
		return
	}
	sb := buildSandboxOf(m)
	if !sb.set() {
		return
	}
	url := strings.TrimSuffix(sb.Endpoint, "/") + "/" + filterPingVersion + "/networks/" + sb.Network
	code, body, err := in.httpGet(ctx, url, nil)
	if err != nil {
		c.fail(sandboxNetworkCheck, "the runner filter at %s does not answer; start it before `sisf up`", sb.Endpoint)
		return
	}
	var view struct {
		Name     string `json:"Name"`
		Internal bool   `json:"Internal"`
	}
	switch {
	case code == http.StatusNotFound:
		c.fail(sandboxNetworkCheck, "the sandbox network %s does not exist; create it with `docker network create --internal %s`", sb.Network, sb.Network)
	case code != http.StatusOK || json.Unmarshal(body, &view) != nil || view.Name != sb.Network:
		c.fail(sandboxNetworkCheck, "the runner filter at %s did not report the sandbox network %s (HTTP %d); it must be a runner filter for that network", sb.Endpoint, sb.Network, code)
	default:
		reportSandboxNetwork(c, sb.Network, view.Internal)
	}
}

func reportSandboxNetwork(c *collector, name string, internal bool) {
	if !internal {
		c.fail(sandboxNetworkCheck, "%s is not internal: a runner on it reaches the internet around the egress proxy, and candidates are not built there; recreate it with `docker network create --internal`", name)
		return
	}
	c.ok(sandboxNetworkCheck, "%s is internal: candidate builds reach out only through the egress proxy", name)
}
