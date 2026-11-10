//go:build smoke

package smoke

import (
	"cmp"
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The Docker build sandbox of the run. A Standalone install builds code an
// agent wrote only in a sandbox of an isolating backend, never on the host;
// the operator provides that sandbox and names it to `sisf init` (see the
// quickstart). The run provides what an operator would:
//
//   - an internal sandbox network, whose only way out is an egress proxy:
//     the product's edge, in a container on that network and an outbound one;
//   - the runner image (sandbox/runner.Dockerfile), pushed to a registry on
//     loopback so the docker backend can run it pinned by its digest;
//   - a read-only git mirror of the private sandbox repository on the
//     sandbox network, kept in step with GitHub from the host with the
//     operator's gh login, since a build holds no secret to fetch with;
//   - the runner Docker request filter, `sisf docker-filter --role runner`,
//     on a loopback port;
//   - the QA agent's launcher (sandbox/qa-codex-launcher.sh) and its image.
//
// Every container, network and image it makes is named with the prefix and
// removed when the run ends.
const (
	dockerPrefixEnv     = "SISF_SMOKE_DOCKER_PREFIX"
	defaultDockerPrefix = "sisf-smoke-"
	sandboxBuildCommand = "/usr/local/bin/sisf-smoke-build"
	egressProxyURL      = "http://egress:3128"
	provisionLimit      = 20 * time.Minute
)

// buildSandbox is the run's sandbox as `sisf init` is told of it.
type buildSandbox struct {
	name        string // prefix of everything made, unique to the run
	network     string // internal sandbox network
	outNetwork  string // the egress proxy's way out
	registry    string // 127.0.0.1:port
	runnerImage string // pinned by digest
	edgeImage   string
	qaImage     string
	endpoint    string // the runner filter's URL
	launcher    string // the QA agent's launcher
	mirror      string // bare repository the git mirror serves
	socket      string // the Docker daemon's socket, for the filter
	dockerHost  string
	docker      string // absolute path of the docker CLI
}

// newBuildSandbox names the run's sandbox and picks its loopback ports.
func newBuildSandbox(r *commandRunner, root string) *buildSandbox {
	r.t.Helper()
	name := cmp.Or(os.Getenv(dockerPrefixEnv), defaultDockerPrefix) + strconv.FormatInt(time.Now().Unix(), 36)
	docker, err := lookPath("docker")
	if err != nil {
		r.fatalf("preflight: the docker CLI is not on PATH: %v", err)
	}
	return &buildSandbox{
		name: name, network: name + "-sandbox", outNetwork: name + "-out",
		registry: "127.0.0.1:" + strconv.Itoa(freePort(r)), endpoint: "http://127.0.0.1:" + strconv.Itoa(freePort(r)),
		launcher: filepath.Join(root, "bin", "qa-codex-launcher"), mirror: filepath.Join(root, "mirror"), docker: docker,
	}
}

// freePort returns a loopback port nothing listens on, outside the
// factory's own 7460-7469.
func freePort(r *commandRunner) int {
	r.t.Helper()
	for range 20 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			r.fatalf("no free loopback port: %v", err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		if port < 7460 || port > 7469 {
			return port
		}
	}
	r.fatalf("no free loopback port outside 7460-7469")
	return 0
}

// dockerEndpoint finds the daemon the docker CLI talks to: its host for the
// launcher and, for the filter, its Unix socket.
func (s *buildSandbox) dockerEndpoint(ctx context.Context, r *commandRunner) {
	r.t.Helper()
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		out, err := r.run(ctx, time.Minute, nil, s.docker, "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
		if err != nil {
			r.fatalf("the docker context cannot be read: %v", err)
		}
		host = strings.TrimSpace(out)
	}
	socket, ok := strings.CutPrefix(host, "unix://")
	if !ok || !filepath.IsAbs(socket) {
		r.fatalf("the Docker daemon is not on a local Unix socket (%s); the runner filter needs one", host)
	}
	s.dockerHost, s.socket = host, socket
}

// provision builds the images and starts the networks, the registry, the
// egress proxy and the git mirror. origin is what the mirror follows.
func (s *buildSandbox) provision(ctx context.Context, r *commandRunner, origin, codexVersion string) {
	r.t.Helper()
	s.dockerEndpoint(ctx, r)
	r.t.Cleanup(func() { s.teardown(r) })
	d := func(limit time.Duration, args ...string) string {
		r.t.Helper()
		out, err := r.run(ctx, limit, nil, s.docker, args...)
		if err != nil {
			r.fatalf("build sandbox: %v", err)
		}
		return strings.TrimSpace(out)
	}
	d(time.Minute, "network", "create", "--internal", s.network)
	d(time.Minute, "network", "create", s.outNetwork)
	d(5*time.Minute, "run", "-d", "--name", s.name+"-registry", "-p", s.registry+":5000", "registry:2")
	s.waitForHTTP(ctx, r, "http://"+s.registry+"/v2/")

	dir := filepath.Join(repoRoot(), "tests", "smoke", "sandbox")
	tag := s.registry + "/sisf-smoke-runner:" + s.name
	d(provisionLimit, "build", "-q", "-f", filepath.Join(dir, "runner.Dockerfile"), "-t", tag, dir)
	d(5*time.Minute, "push", "-q", tag)
	for _, ref := range strings.Fields(d(time.Minute, "image", "inspect", "--format", `{{join .RepoDigests " "}}`, tag)) {
		if strings.HasPrefix(ref, s.registry+"/sisf-smoke-runner@sha256:") {
			s.runnerImage = ref
		}
	}
	if s.runnerImage == "" {
		r.fatalf("build sandbox: the runner image has no digest from the registry")
	}
	s.edgeImage, s.qaImage = s.name+"-edge:run", s.name+"-qa-codex:run"
	d(provisionLimit, "build", "-q", "-f", filepath.Join(repoRoot(), "build", "edge.Dockerfile"), "-t", s.edgeImage, repoRoot())
	d(provisionLimit, "build", "-q", "-f", filepath.Join(dir, "qa-codex.Dockerfile"), "--build-arg", "CODEX_VERSION="+codexVersion, "-t", s.qaImage, dir)

	hardened := []string{"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges"}
	d(time.Minute, append(append([]string{"create", "--name", s.name + "-egress", "--network", s.outNetwork}, hardened...),
		s.edgeImage, "edge", "--listen=127.0.0.1:8080", "--upstream=http://127.0.0.1:9", "--egress-listen=egress:3128", "--allow-non-loopback")...)
	d(time.Minute, "network", "connect", "--alias", "egress", s.network, s.name+"-egress")
	d(time.Minute, "start", s.name+"-egress")

	s.startMirror(ctx, r, origin)
	d(time.Minute, append(append([]string{"run", "-d", "--name", s.name + "-git", "--network", s.network, "--network-alias", "git-mirror",
		"--user", "65534:65534", "-e", "GIT_CONFIG_COUNT=1", "-e", "GIT_CONFIG_KEY_0=safe.directory", "-e", "GIT_CONFIG_VALUE_0=*",
		"-v", s.mirror + ":/srv/git:ro"}, hardened...), s.runnerImage, "git", "daemon", "--base-path=/srv/git", "--export-all", "--reuseaddr", "/srv/git")...)
	s.writeLauncher(r)
}

// teardown removes everything the run made, and nothing else.
func (s *buildSandbox) teardown(r *commandRunner) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ids := map[string]bool{}
	for _, filter := range []string{"name=^" + s.name + "-", "label=sisf.smoke=" + s.name, "network=" + s.network} {
		out, _ := r.run(ctx, time.Minute, nil, s.docker, "ps", "-aq", "--filter", filter)
		for _, id := range strings.Fields(out) {
			ids[id] = true
		}
	}
	for id := range ids {
		_, _ = r.run(ctx, time.Minute, nil, s.docker, "rm", "-f", id)
	}
	for _, network := range []string{s.network, s.outNetwork} {
		_, _ = r.run(ctx, time.Minute, nil, s.docker, "network", "rm", network)
	}
	for _, image := range []string{s.edgeImage, s.qaImage, s.registry + "/sisf-smoke-runner:" + s.name} {
		if image != "" {
			_, _ = r.run(ctx, time.Minute, nil, s.docker, "image", "rm", image)
		}
	}
}
