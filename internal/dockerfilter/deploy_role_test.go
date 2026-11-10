package dockerfilter_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/dockerfilter"
	"github.com/bestagentkits/sisf/internal/dockerfilter/dockerfiltertest"
)

const (
	deployNetwork = "acme-app-app"
	deployVersion = "/v1.43"
)

var (
	unitImage = "sha256:" + strings.Repeat("c", 64)
	digest    = "sha256:" + strings.Repeat("d", 64)
	// revisionName is the backend's container name of digest.
	revisionName = factory + "-app-" + strings.Repeat("d", 12)
)

// deployEnv is a deploy filter in front of a fake daemon holding the
// factory's control container and one of its runners.
type deployEnv struct {
	env
	runner string
}

func newDeployEnv(t *testing.T, sim map[string]string) deployEnv {
	t.Helper()
	daemon := dockerfiltertest.New("runc")
	upstream := httptest.NewServer(daemon)
	t.Cleanup(upstream.Close)
	f, err := dockerfilter.New(dockerfilter.Config{
		Role: dockerfilter.RoleDeploy, Upstream: upstream.Client().Transport, UpstreamURL: upstream.URL,
		RunnerValue: factory, DeployNetwork: deployNetwork, Images: []string{unitImage}, SimulatedImages: sim,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	control := daemon.AddContainer("acme-app-control", map[string]string{"com.docker.compose.service": "control", "sisf.factory": factory})
	runner := daemon.AddContainer("sisf-runner-1", map[string]string{"sisf.runner": factory, "sisf.factory": factory,
		"sisf.unit": "app", "sisf.digest": digest})
	return deployEnv{env: env{daemon: daemon, filter: srv, control: control}, runner: runner}
}

// deployCreate is the deployment backend's create body for digest.
func deployCreate() map[string]any {
	return map[string]any{
		"Image": unitImage, "Env": []string{"SISF_REVISION=" + digest},
		"Labels": map[string]any{"sisf.factory": factory, "sisf.unit": "app", "sisf.digest": digest},
		"HostConfig": map[string]any{
			"NetworkMode": deployNetwork, "RestartPolicy": map[string]any{"Name": "unless-stopped"}, "ReadonlyRootfs": true,
			"CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges"},
			"Tmpfs": map[string]any{"/tmp": "rw,noexec,nosuid,size=64m"},
		},
	}
}

func (e deployEnv) createRevision(t *testing.T, name string, body map[string]any) (int, string) {
	t.Helper()
	return e.do(t, http.MethodPost, deployVersion+"/containers/create?name="+name, encode(t, body))
}

func TestTheDeployRoleRunsTheRevisionsOfTheFactorysUnits(t *testing.T) {
	t.Parallel()
	e := newDeployEnv(t, nil)
	status, out := e.createRevision(t, revisionName, deployCreate())
	mustStatus(t, status, http.StatusCreated, "create a revision", out)
	filters := url.QueryEscape(`{"label":["sisf.factory=` + factory + `","sisf.unit=app"]}`)
	for _, step := range []struct{ method, path string }{
		{http.MethodGet, "/containers/" + revisionName + "/json"},
		{http.MethodPost, "/containers/" + revisionName + "/start"},
		{http.MethodGet, "/containers/" + revisionName + "/logs?stdout=1&stderr=1&since=1700000000"},
		{http.MethodGet, "/containers/json?all=1&filters=" + filters},
		{http.MethodPost, "/containers/" + revisionName + "/stop?t=5"},
		{http.MethodDelete, "/containers/" + revisionName + "?force=1"},
	} {
		status, out := e.do(t, step.method, deployVersion+step.path, "")
		if status >= 300 {
			t.Fatalf("%s %s: %d %s", step.method, step.path, status, out)
		}
		if strings.HasPrefix(step.path, "/containers/json") && !strings.Contains(out, revisionName) {
			t.Fatalf("the list does not hold the revision: %s", out)
		}
	}
	if len(e.daemon.Containers()) != 2 {
		t.Fatalf("the revision was not removed: %+v", e.daemon.Containers())
	}
}

func TestTheDeployRoleRunsASimulatedCandidateAsItsUnitsImage(t *testing.T) {
	t.Parallel()
	e := newDeployEnv(t, map[string]string{"app": unitImage})
	body := deployCreate()
	body["Image"] = "sim.local/" + factory + "/app@" + digest
	status, out := e.createRevision(t, revisionName, body)
	mustStatus(t, status, http.StatusCreated, "create a simulated candidate", out)
	created := e.daemon.Created()
	if len(created) != 1 || !strings.Contains(string(created[0].Body), unitImage) || strings.Contains(string(created[0].Body), "sim.local") {
		t.Fatalf("the daemon was asked for %s", created[0].Body)
	}
	// Without the simulation setting the simulated reference is refused.
	plain := newDeployEnv(t, nil)
	status, out = plain.createRevision(t, revisionName, body)
	mustStatus(t, status, http.StatusForbidden, "a simulated reference outside the simulation", out)
}

// Every widening of a deployed revision is refused before the daemon sees it.
func TestTheDeployRoleRefusesWhatADeploymentDoesNotNeed(t *testing.T) {
	t.Parallel()
	creates := map[string]func(b map[string]any) string{
		"another network":  func(b map[string]any) string { host(b)["NetworkMode"] = "acme-app-control"; return revisionName },
		"the host network": func(b map[string]any) string { host(b)["NetworkMode"] = "host"; return revisionName },
		"a bind mount":     func(b map[string]any) string { host(b)["Binds"] = []string{"/:/host"}; return revisionName },
		"a named volume": func(b map[string]any) string {
			host(b)["Mounts"] = []any{map[string]any{"Type": "volume", "Source": "acme-app-state-data", "Target": "/data"}}
			return revisionName
		},
		"privileged":        func(b map[string]any) string { host(b)["Privileged"] = true; return revisionName },
		"a capability":      func(b map[string]any) string { host(b)["CapAdd"] = []string{"NET_ADMIN"}; return revisionName },
		"capabilities kept": func(b map[string]any) string { host(b)["CapDrop"] = []string{"NET_RAW"}; return revisionName },
		"a writable root":   func(b map[string]any) string { host(b)["ReadonlyRootfs"] = false; return revisionName },
		"seccomp off": func(b map[string]any) string {
			host(b)["SecurityOpt"] = []string{"seccomp=unconfined"}
			return revisionName
		},
		"a published port": func(b map[string]any) string {
			host(b)["PortBindings"] = map[string]any{"80/tcp": []any{}}
			return revisionName
		},
		"always restart": func(b map[string]any) string {
			host(b)["RestartPolicy"] = map[string]any{"Name": "always"}
			return revisionName
		},
		"another image": func(b map[string]any) string {
			b["Image"] = "alpine@sha256:" + strings.Repeat("e", 64)
			return revisionName
		},
		"a secret in env": func(b map[string]any) string {
			b["Env"] = []string{"SISF_REVISION=" + digest, "SISF_GITHUB_TOKEN=x"}
			return revisionName
		},
		"another revision": func(b map[string]any) string {
			b["Env"] = []string{"SISF_REVISION=sha256:" + strings.Repeat("f", 64)}
			return revisionName
		},
		"a runner label": func(b map[string]any) string {
			b["Labels"].(map[string]any)["sisf.runner"] = factory
			return revisionName
		},
		"another factory": func(b map[string]any) string {
			b["Labels"].(map[string]any)["sisf.factory"] = "other"
			return revisionName
		},
		"a foreign name": func(map[string]any) string { return "acme-app-control" },
		"a root user":    func(b map[string]any) string { b["User"] = "0"; return revisionName },
		"a command":      func(b map[string]any) string { b["Cmd"] = []string{"sh"}; return revisionName },
	}
	for name, mutate := range creates {
		t.Run("create with "+name, func(t *testing.T) {
			t.Parallel()
			e := newDeployEnv(t, nil)
			body := deployCreate()
			target := mutate(body)
			status, out := e.createRevision(t, target, body)
			mustStatus(t, status, http.StatusForbidden, name, out)
			if len(e.daemon.Created()) != 0 {
				t.Fatal("the refused create reached the daemon")
			}
		})
	}
	e := newDeployEnv(t, nil)
	for name, req := range map[string]struct{ method, path string }{
		"inspect of the control container":  {http.MethodGet, "/containers/" + e.control + "/json"},
		"stop of the control container":     {http.MethodPost, "/containers/acme-app-control/stop"},
		"remove of a runner":                {http.MethodDelete, "/containers/" + e.runner + "?force=1"},
		"a list of every container":         {http.MethodGet, "/containers/json?all=1"},
		"a list of another factory":         {http.MethodGet, "/containers/json?filters=" + url.QueryEscape(`{"label":["sisf.factory=other"]}`)},
		"a list filtered by name":           {http.MethodGet, "/containers/json?filters=" + url.QueryEscape(`{"label":["sisf.factory=`+factory+`"],"name":["control"]}`)},
		"wait":                              {http.MethodPost, "/containers/" + revisionName + "/wait"},
		"info":                              {http.MethodGet, "/info"},
		"exec":                              {http.MethodPost, "/containers/" + revisionName + "/exec"},
		"an image pull":                     {http.MethodPost, "/images/create?fromImage=alpine"},
		"removing volumes with a container": {http.MethodDelete, "/containers/" + revisionName + "?force=1&v=1"},
	} {
		before := len(e.daemon.Requests())
		status, out := e.do(t, req.method, deployVersion+req.path, "")
		if status != http.StatusForbidden && status != http.StatusNotFound {
			t.Errorf("%s: status %d (%s)", name, status, strings.TrimSpace(out))
		}
		for _, r := range e.daemon.Requests()[before:] {
			if r.Method != http.MethodGet || !strings.HasSuffix(r.Path, "/json") || strings.HasSuffix(r.Path, "/containers/json") {
				t.Errorf("%s: %s %s reached the daemon", name, r.Method, r.Path)
			}
		}
	}
}

func TestADeployFilterNeedsItsOwnSettings(t *testing.T) {
	t.Parallel()
	transport := http.DefaultTransport
	for name, cfg := range map[string]dockerfilter.Config{
		"no deploy network":              {Role: dockerfilter.RoleDeploy, Upstream: transport, RunnerValue: factory, Images: []string{unitImage}},
		"a sandbox network":              {Role: dockerfilter.RoleDeploy, Upstream: transport, RunnerValue: factory, Images: []string{unitImage}, DeployNetwork: deployNetwork, SandboxNetwork: network},
		"an unpinned image":              {Role: dockerfilter.RoleDeploy, Upstream: transport, RunnerValue: factory, Images: []string{"hello:latest"}, DeployNetwork: deployNetwork},
		"a simulated foreign image":      {Role: dockerfilter.RoleDeploy, Upstream: transport, RunnerValue: factory, Images: []string{unitImage}, DeployNetwork: deployNetwork, SimulatedImages: map[string]string{"app": image}},
		"a runner with a deploy network": {Upstream: transport, RunnerValue: factory, Images: []string{image}, SandboxNetwork: network, DeployNetwork: deployNetwork},
		"an unknown role":                {Role: "admin", Upstream: transport, RunnerValue: factory, Images: []string{image}, SandboxNetwork: network},
	} {
		if _, err := dockerfilter.New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
