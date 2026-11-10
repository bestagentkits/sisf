package install

import (
	"errors"
	"github.com/bestagentkits/sisf/internal/signing"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

const dockerPS = `{"Service":"state","State":"running","Health":"healthy"}
{"Service":"supervisor","State":"running","Health":"healthy"}
{"Service":"app","State":"running","Health":"healthy"}
{"Service":"control","State":"running","Health":"healthy"}
{"Service":"dispatcher","State":"running","Health":"healthy"}
{"Service":"edge","State":"running","Health":""}
`

const supervisorJSON = `{"factory_id":"sisf-hello-app","self_check":{"passed":true},"lkg":{"recorded":true}}`

// dockerHost is what the fake Docker host answers about the release
// topology: who is on the docker network, who reaches the filters and the
// edge's admin listener, what the edge publishes, and the trust volume.
type dockerHost struct {
	dockerNetwork    string
	dispatcherReach  bool
	edgePorts        string
	adminRefusal     string
	missingImage     bool
	trust            map[string][]byte
	supervisorBlocks bool
	// sandboxInternal is what the sandbox network reports as its internal
	// flag; empty when the network does not exist.
	sandboxInternal string
}

// healthyDockerHost is a host on which every topology check passes; the
// trust volume holds the delegations `sisf up` issues.
func healthyDockerHost(t *testing.T, in *Installer) dockerHost {
	t.Helper()
	m, err := in.Layout.LoadManifest()
	must(t, err)
	files, err := in.trustMaterial(t.Context(), m.FactoryID, signing.SignedManifest{})
	must(t, err)
	return dockerHost{
		dockerNetwork: "sisf-hello-app-supervisor sisf-hello-app-docker-filter-runner sisf-hello-app-docker-filter-deploy",
		edgePorts:     "7460/tcp=127.0.0.1:7460 ", adminRefusal: "wget: server returned error: HTTP/1.1 401 Unauthorized", trust: files,
		sandboxInternal: "true",
	}
}

// answer is the fake Docker CLI of a running project.
func (h dockerHost) answer(ps, status string) func(Cmd) ([]byte, error) {
	return func(c Cmd) ([]byte, error) {
		args := strings.Join(c.Args, " ")
		switch {
		case strings.Contains(args, " ps "):
			return []byte(ps), nil
		case strings.Contains(args, " exec -T supervisor supervisor status"):
			if status == "" {
				return nil, errors.New("no such container")
			}
			return []byte(status), nil
		case strings.HasPrefix(args, "network inspect --format {{.Internal}} "):
			if h.sandboxInternal == "" {
				return nil, errors.New("Error response from daemon: network not found")
			}
			return []byte(h.sandboxInternal + "\n"), nil
		case strings.HasPrefix(args, "network inspect"):
			return []byte(h.dockerNetwork), nil
		case strings.HasPrefix(args, "inspect --format"):
			return []byte(h.edgePorts), nil
		case strings.HasPrefix(args, "image inspect"):
			if h.missingImage {
				return nil, errors.New("no such image")
			}
			return []byte("sha256:x"), nil
		case strings.Contains(args, " exec -T supervisor wget") && strings.Contains(args, "edge-admin"):
			return nil, errors.New("docker compose: exit status 1: " + h.adminRefusal)
		case strings.Contains(args, " exec -T supervisor wget"):
			if h.supervisorBlocks {
				return nil, errors.New("exit status 1")
			}
			return nil, nil
		case strings.Contains(args, " exec -T dispatcher wget"):
			if h.dispatcherReach {
				return nil, nil
			}
			return []byte("wget: bad address"), errors.New("exit status 1")
		case strings.Contains(args, " exec -T supervisor cat /var/lib/sisf/trust/"):
			data, ok := h.trust[c.Args[len(c.Args)-1][len("/var/lib/sisf/trust/"):]]
			if !ok {
				return nil, errors.New("no such file")
			}
			return data, nil
		}
		return nil, nil
	}
}

// dockerFactory is a Docker install that was brought up with fakes; ps is
// what `docker compose ps` answers afterwards.
func dockerFactory(t *testing.T, ps, status string) *Installer {
	t.Helper()
	return dockerFactoryOn(t, ps, status, nil)
}

// dockerFactoryOn is dockerFactory on a host the test may change.
func dockerFactoryOn(t *testing.T, ps, status string, change func(*dockerHost)) *Installer {
	t.Helper()
	in, exec, _ := newTestInstaller(t)
	exec.answer = buildAnswers
	if err := in.Up(t.Context(), UpOptions{Init: initOptions(in, manifest.ModeDocker)}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	host := healthyDockerHost(t, in)
	if change != nil {
		change(&host)
	}
	exec.answer = host.answer(ps, status)
	in.HTTP = &http.Client{Transport: healthyNet().without("127.0.0.1:7465/healthz"), Timeout: time.Second}
	return in
}

func TestAHealthyDockerFactoryPassesAndSeesZonesThroughTheirContainers(t *testing.T) {
	t.Parallel()
	in := dockerFactory(t, dockerPS, supervisorJSON)
	r := runDoctor(t, in)
	for _, c := range r.Checks {
		if c.Verdict != VerdictOK && c.Name != "artifacts" {
			t.Errorf("%s: %s %s", c.Name, c.Verdict, c.Detail)
		}
	}
	for _, name := range []string{"container app", "container dispatcher", "container state", "compose isolation",
		"docker filters", "edge admin", "runner images", "sandbox network", "delegated keys", "lifecycle"} {
		if len(verdictOf(r, name)) == 0 {
			t.Errorf("no %q check ran", name)
		}
	}
	// The supervisor's own client runs inside its container; the host never
	// reaches the admin address.
	found := false
	for _, l := range in.Exec.(*fakeExec).lines() {
		found = found || strings.Contains(l, "exec -T supervisor supervisor status --admin=unix:/run/sisf/admin/supervisor.sock")
	}
	if !found {
		t.Error("rollback readiness was not read from the supervisor container")
	}
}

func TestAnUnhealthyOrStoppedContainerFailsTheReport(t *testing.T) {
	t.Parallel()
	ps := strings.Replace(dockerPS, `"Service":"app","State":"running","Health":"healthy"`, `"Service":"app","State":"running","Health":"unhealthy"`, 1)
	ps = strings.Replace(ps, `"Service":"dispatcher","State":"running"`, `"Service":"dispatcher","State":"exited"`, 1)
	r := runDoctor(t, dockerFactory(t, ps, supervisorJSON))
	if r.OK() {
		t.Fatal("the report passed")
	}
	for _, name := range []string{"container app", "container dispatcher"} {
		if v := verdictOf(r, name); len(v) != 1 || v[0] != VerdictFail {
			t.Errorf("%s = %v", name, v)
		}
	}
}

func TestADockerFactoryWithoutContainersSaysHowToStartIt(t *testing.T) {
	t.Parallel()
	r := runDoctor(t, dockerFactory(t, "", ""))
	if v := verdictOf(r, "containers"); len(v) != 1 || v[0] != VerdictFail || !strings.Contains(detailOf(r, "containers"), "sisf up") {
		t.Fatalf("containers = %v %q", v, detailOf(r, "containers"))
	}
	if v := verdictOf(r, "rollback"); len(v) != 1 || v[0] != VerdictFail {
		t.Fatalf("rollback = %v", v)
	}
}

func TestADockerProjectEditedToExposeTheSocketFailsIsolation(t *testing.T) {
	t.Parallel()
	in := dockerFactory(t, dockerPS, supervisorJSON)
	// The dispatcher is handed the raw Docker socket.
	editFile(t, in.Layout.Compose(), "  dispatcher:\n", "  dispatcher:\n    volumes: [\"/var/run/docker.sock:/var/run/docker.sock\"]\n")
	r := runDoctor(t, in)
	if v := verdictOf(r, "compose isolation"); len(v) == 0 || v[0] != VerdictFail {
		t.Fatalf("compose isolation = %v", v)
	}
	if r.OK() {
		t.Fatal("the report passed")
	}
}

func TestBothComposePSFormatsAreRead(t *testing.T) {
	t.Parallel()
	lines, err := parseComposePS([]byte(dockerPS))
	if err != nil || len(lines) != 6 {
		t.Fatalf("one object per line: %d %v", len(lines), err)
	}
	array := "[" + strings.ReplaceAll(strings.TrimSpace(dockerPS), "}\n{", "},{") + "]"
	arr, err := parseComposePS([]byte(array))
	if err != nil || len(arr) != 6 {
		t.Fatalf("one array: %d %v", len(arr), err)
	}
	if _, err := parseComposePS([]byte("not json")); err == nil {
		t.Fatal("garbage was accepted")
	}
}
