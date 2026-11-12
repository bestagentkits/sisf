//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// No Docker API reaches the dispatcher: it has no socket file, no
// DOCKER_HOST, and no route to a socket proxy or to a daemon on a TCP port.
// Like the lifecycle test it runs against the install in $SISF_HOME and
// changes nothing.
func TestTheDispatcherContainerHasNoDockerEndpoint(t *testing.T) {
	d := newComposeDeployment(t)
	dispatcher := d.container("dispatcher")

	env, err := d.docker(t, "exec", dispatcher, "env")
	if err != nil {
		t.Fatalf("cannot read the environment of %s: %v\n%s", dispatcher, err, env)
	}
	for _, line := range strings.Split(env, "\n") {
		if strings.HasPrefix(line, "DOCKER_HOST=") {
			t.Errorf("the dispatcher has %s", line)
		}
	}

	for _, path := range []string{"/var/run/docker.sock", "/run/docker.sock", "/run/podman/podman.sock", "/run/containerd/containerd.sock"} {
		out, err := d.docker(t, "exec", dispatcher, "ls", "-d", path)
		if err == nil {
			t.Errorf("%s exists in the dispatcher container: %s", path, out)
		}
	}

	// The proxy's old name must not resolve, and neither the proxy port nor
	// the Docker API of the host's gateway may answer.
	for _, url := range []string{
		"http://socket-proxy:2375/version",
		"http://host.docker.internal:2375/version",
		"http://127.0.0.1:2375/version",
	} {
		if out, err := d.docker(t, "exec", dispatcher, "wget", "-q", "-T", "3", "-O", "-", url); err == nil {
			t.Errorf("%s answered from inside the dispatcher: %s", url, out)
		}
	}
}
