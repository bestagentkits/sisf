package install

import (
	"net"
	"net/url"
	"regexp"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Extension keys of the build sandbox of a Standalone install.
const (
	extBuildDockerEndpoint = "build_docker_endpoint"
	extBuildDockerNetwork  = "build_docker_network"
	extBuildEgressProxy    = "build_egress_proxy"
	extBuildRunnerImage    = "build_runner_image"
)

// BuildSandbox is the Docker sandbox a Standalone install builds candidates
// in. The supervisor builds code an agent wrote only in a sandbox of an
// isolating backend, never with the local one; a Standalone install that
// runs real agents therefore builds nothing until it is given one. The
// operator provides the sandbox: a runner Docker request filter (`sisf
// docker-filter --role runner`) on a loopback address, an internal sandbox
// network, an egress proxy on that network as its only way out, and the
// runner image the filter allows. The agents still run as local processes
// under trusted_code_only; only the builds move into the sandbox, and the
// QA agent, which reads those untrusted candidates, then needs a
// sandboxing launcher (--qa-agent-binary) as it does in Docker mode.
type BuildSandbox struct {
	// Endpoint is the runner filter's URL, http on a loopback address.
	Endpoint string
	// Network is the internal sandbox network the runners join.
	Network string
	// EgressProxy is the egress proxy's URL on the sandbox network.
	EgressProxy string
	// RunnerImage is the image candidate builds run in, as name@sha256:...:
	// the docker backend runs only an image pinned by its repository
	// digest, so a locally built image is first pushed to a registry, such
	// as one on loopback.
	RunnerImage string
}

// set reports whether any part of the sandbox is configured.
func (b BuildSandbox) set() bool { return b != BuildSandbox{} }

var (
	dockerNetworkPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
	// egressProxyPattern is what the docker execution backend accepts.
	egressProxyPattern = regexp.MustCompile(`^http://[A-Za-z0-9.-]+(:[0-9]{1,5})?/?$`)
	// runnerImagePattern is what the docker backend runs
	// (adapters/exec/docker/sandbox.go): a reference pinned by its registry
	// digest, name@sha256:<digest>, or the full ID of an image built on this
	// host, sha256:<64 hex>. A tag and a short ID are refused.
	runnerImagePattern = regexp.MustCompile(`^(?:[a-z0-9]+(?:[._/:-][a-z0-9]+)*@)?sha256:[a-f0-9]{64}$`)
)

// check validates the sandbox on its own: every part given and well formed.
func (b BuildSandbox) check() error {
	if b.Endpoint == "" || b.Network == "" || b.EgressProxy == "" || b.RunnerImage == "" {
		return invalidf("a build sandbox needs all of --exec-docker-endpoint, --exec-docker-network, --egress-proxy and --runner-image")
	}
	u, err := url.Parse(b.Endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return invalidf("--exec-docker-endpoint must be the runner filter's http URL on a loopback address")
	}
	host, port, err := net.SplitHostPort(u.Host)
	if ip := net.ParseIP(host); err != nil || port == "" || ip == nil || !ip.IsLoopback() {
		return invalidf("--exec-docker-endpoint must be the runner filter's http URL on a loopback address")
	}
	if !dockerNetworkPattern.MatchString(b.Network) {
		return invalidf("--exec-docker-network must be a Docker network name")
	}
	if !egressProxyPattern.MatchString(b.EgressProxy) {
		return invalidf("--egress-proxy must be a plain http URL of a host on the sandbox network")
	}
	if !runnerImagePattern.MatchString(b.RunnerImage) {
		return invalidf("--runner-image must pin the image by content: name@sha256:<digest> or a full local image ID, sha256:<64 hex>; a tag or a short ID is refused")
	}
	return nil
}

// normalize checks the build sandbox against the rest of the options: it
// is for a production Standalone install, it needs the build command and
// the QA agent's sandboxing launcher, and with it the QA agent's home is
// the launcher's, so --qa-home is refused.
func (b BuildSandbox) normalize(o Options) error {
	if !b.set() {
		return nil
	}
	switch {
	case o.Mode != manifest.ModeStandalone:
		return invalidf("--exec docker with a build sandbox is for a Standalone install; Docker mode runs its own")
	case o.Sim:
		return invalidf("the simulation builds nothing; a build sandbox is for real agents")
	case o.BuildCommand == "":
		return invalidf("a build sandbox needs --build-command, the command that builds a candidate in it")
	case o.QAAgentBinary == "":
		return invalidf("a build sandbox needs --qa-agent-binary: the QA agent reads the untrusted candidates and must run in a sandbox too")
	case o.Agents.QAHome != "":
		return invalidf("--qa-home is refused with a build sandbox: the QA agent's launcher owns its home")
	}
	return b.check()
}

// extensions records the sandbox in the manifest.
func (b BuildSandbox) extensions(ext map[string]any) {
	for key, value := range map[string]string{
		extBuildDockerEndpoint: b.Endpoint, extBuildDockerNetwork: b.Network,
		extBuildEgressProxy: b.EgressProxy, extBuildRunnerImage: b.RunnerImage,
	} {
		if value != "" {
			ext[key] = value
		}
	}
}

// buildSandboxOf reads the sandbox of an install.
func buildSandboxOf(m *manifest.Manifest) BuildSandbox {
	get := func(key string) string { v, _ := m.Extensions[key].(string); return v }
	return BuildSandbox{Endpoint: get(extBuildDockerEndpoint), Network: get(extBuildDockerNetwork),
		EgressProxy: get(extBuildEgressProxy), RunnerImage: get(extBuildRunnerImage)}
}

// supervisorArgs are the supervisor's execution flags: the docker backend
// behind the runner filter when a sandbox is configured, else the local
// backend, which builds nothing an agent wrote.
func (b BuildSandbox) supervisorArgs() []string {
	if !b.set() {
		return []string{"--exec=local"}
	}
	return []string{
		"--exec=docker", "--exec-docker-endpoint=" + b.Endpoint, "--exec-docker-network=" + b.Network,
		"--egress-proxy=" + b.EgressProxy, "--runner-image=" + b.RunnerImage,
	}
}
