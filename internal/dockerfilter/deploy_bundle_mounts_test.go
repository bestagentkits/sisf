package dockerfilter_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/dockerfilter"
	"github.com/bestagentkits/sisf/internal/dockerfilter/dockerfiltertest"
)

const (
	bundleTarget = "/var/www/html"
	uploadsName  = "acme-app-uploads"
	uploadsAt    = "/var/www/html/wp-content/uploads"
)

var (
	bundleDigest = "sha256:" + strings.Repeat("b", 64)
	bundleVolume = factory + "-bundle-" + strings.Repeat("b", 12)
	loaderName   = factory + "-loader-" + strings.Repeat("b", 12)
	runtimeEnv   = []string{"APACHE_RUN_DIR=/tmp/apache2", "WORDPRESS_DB_HOST=db"}
)

// newBundleEnv is a deploy filter that allows a bundle at bundleTarget, the
// uploads data volume and the runtime environment. tweak changes the
// configuration before the filter is built.
func newBundleEnv(t *testing.T, tweak func(*dockerfilter.Config)) deployEnv {
	t.Helper()
	daemon := dockerfiltertest.New("runc")
	upstream := httptest.NewServer(daemon)
	t.Cleanup(upstream.Close)
	cfg := dockerfilter.Config{
		Role: dockerfilter.RoleDeploy, Upstream: upstream.Client().Transport, UpstreamURL: upstream.URL,
		RunnerValue: factory, DeployNetwork: deployNetwork, Images: []string{unitImage},
		BundleTarget: bundleTarget, DataVolumes: []dockerfilter.DeployVolume{{Name: uploadsName, Target: uploadsAt}},
		DeployEnv: runtimeEnv, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if tweak != nil {
		tweak(&cfg)
	}
	f, err := dockerfilter.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	runner := daemon.AddContainer("sisf-runner-1", map[string]string{"sisf.runner": factory, "sisf.factory": factory})
	return deployEnv{env: env{daemon: daemon, filter: srv}, runner: runner}
}

// bundleRevision is the backend's create body of a revision that runs the
// bundle over the runtime image as www-data.
func bundleRevision() map[string]any {
	b := deployCreate()
	b["Env"] = append([]string{"SISF_REVISION=" + digest}, runtimeEnv...)
	b["User"] = "33:33"
	host(b)["Mounts"] = []any{
		map[string]any{"Type": "volume", "Source": bundleVolume, "Target": bundleTarget, "ReadOnly": true},
		map[string]any{"Type": "volume", "Source": uploadsName, "Target": uploadsAt},
	}
	return b
}

// loaderCreate is the backend's create body of the loader of bundleDigest.
func loaderCreate() map[string]any {
	return map[string]any{
		"Image":  unitImage,
		"Labels": map[string]any{"sisf.factory": factory, "sisf.loader": "1", "sisf.bundle": bundleDigest},
		"HostConfig": map[string]any{
			"NetworkMode": "none", "RestartPolicy": map[string]any{"Name": "no"}, "ReadonlyRootfs": true,
			"CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges"},
			"Mounts": []any{map[string]any{"Type": "volume", "Source": bundleVolume, "Target": dockerfilter.DeployLoaderTarget}},
			"Memory": 512 << 20, "MemorySwap": 512 << 20, "NanoCpus": 1_000_000_000, "PidsLimit": 512,
		},
	}
}

func volumeCreate() map[string]any {
	return map[string]any{"Name": bundleVolume, "Labels": map[string]any{"sisf.factory": factory, "sisf.bundle": bundleDigest}}
}

// upload sends a tar stream to path through the filter.
func (e deployEnv) upload(t *testing.T, path, contentType string, body []byte) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, e.filter.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func archivePath(container string) string {
	return deployVersion + "/containers/" + container + "/archive?path=" + dockerfilter.DeployLoaderTarget
}

// loaded makes the bundle volume and its loader through the filter.
func (e deployEnv) loaded(t *testing.T) {
	t.Helper()
	status, out := e.do(t, http.MethodPost, deployVersion+"/volumes/create", encode(t, volumeCreate()))
	mustStatus(t, status, http.StatusCreated, "create the bundle volume", out)
	status, out = e.createRevision(t, loaderName, loaderCreate())
	mustStatus(t, status, http.StatusCreated, "create the loader", out)
}

func TestTheDeployRoleLoadsABundleAndRunsARevisionOverIt(t *testing.T) {
	t.Parallel()
	e := newBundleEnv(t, nil)
	status, out := e.do(t, http.MethodGet, deployVersion+"/volumes/"+bundleVolume, "")
	mustStatus(t, status, http.StatusNotFound, "inspect a volume not made yet", out)
	e.loaded(t)

	status, out = e.do(t, http.MethodGet, deployVersion+"/volumes/"+bundleVolume, "")
	mustStatus(t, status, http.StatusOK, "inspect the bundle volume", out)
	if strings.Contains(out, "Mountpoint") || !strings.Contains(out, bundleDigest) {
		t.Errorf("the volume answer must hold its name and labels only: %s", out)
	}
	tarball := []byte("a tar stream of the document root")
	status, out = e.upload(t, archivePath(loaderName), "application/x-tar", tarball)
	mustStatus(t, status, http.StatusOK, "upload the bundle", out)
	// A loader goes with the anonymous volumes its image declares.
	status, out = e.do(t, http.MethodDelete, deployVersion+"/containers/"+loaderName+"?force=1&v=1", "")
	mustStatus(t, status, http.StatusNoContent, "remove the loader", out)

	status, out = e.createRevision(t, revisionName, bundleRevision())
	mustStatus(t, status, http.StatusCreated, "create a revision over the bundle", out)
	status, out = e.do(t, http.MethodPost, deployVersion+"/containers/"+revisionName+"/start", "")
	mustStatus(t, status, http.StatusNoContent, "start the revision", out)
	for _, v := range []string{"1", "true"} {
		before := len(e.daemon.Requests())
		status, out = e.do(t, http.MethodDelete, deployVersion+"/containers/"+revisionName+"?force=1&v="+v, "")
		mustStatus(t, status, http.StatusForbidden, "remove a revision with its volumes", out)
		for _, r := range e.daemon.Requests()[before:] {
			if r.Method != http.MethodGet {
				t.Fatalf("a revision removal with its volumes reached the daemon: %s %s", r.Method, r.Path)
			}
		}
	}

	var archived []dockerfiltertest.Archive
	for _, r := range e.daemon.Requests() {
		if r.Method == http.MethodPut {
			archived = append(archived, dockerfiltertest.Archive{Path: r.Query, Body: r.Body})
		}
	}
	if len(archived) != 1 || !bytes.Equal(archived[0].Body, tarball) || archived[0].Path != "path=/sisf-bundle" {
		t.Fatalf("the daemon received uploads %+v", archived)
	}
	created := e.daemon.Created()
	var forwarded struct {
		HostConfig struct {
			Mounts []map[string]any
		}
	}
	if err := json.Unmarshal(created[len(created)-1].Body, &forwarded); err != nil {
		t.Fatal(err)
	}
	if len(forwarded.HostConfig.Mounts) != 2 || forwarded.HostConfig.Mounts[0]["ReadOnly"] != true {
		t.Fatalf("the revision's mounts were forwarded as %+v", forwarded.HostConfig.Mounts)
	}
	// The bundle volume is in use by the revision, and the daemon keeps it.
	status, _ = e.do(t, http.MethodDelete, deployVersion+"/volumes/"+bundleVolume, "")
	mustStatus(t, status, http.StatusConflict, "remove a bundle volume in use", "")
}

func TestABundleLoaderIsNeverStarted(t *testing.T) {
	t.Parallel()
	e := newBundleEnv(t, nil)
	e.loaded(t)
	for _, path := range []string{"/containers/" + loaderName + "/start", "/containers/" + loaderName + "/stop"} {
		before := len(e.daemon.Requests())
		status, out := e.do(t, http.MethodPost, deployVersion+path, "")
		mustStatus(t, status, http.StatusForbidden, path, out)
		for _, r := range e.daemon.Requests()[before:] {
			if r.Method != http.MethodGet {
				t.Fatalf("%s: %s %s reached the daemon", path, r.Method, r.Path)
			}
		}
	}
	status, out := e.do(t, http.MethodGet, deployVersion+"/containers/"+loaderName+"/logs?stdout=1", "")
	mustStatus(t, status, http.StatusForbidden, "logs of a loader", out)
	for _, c := range e.daemon.Containers() {
		if c.Name == loaderName && c.Started {
			t.Fatal("the loader was started")
		}
	}
}

func TestABundleIsUploadedOnlyIntoALoaderAtItsPath(t *testing.T) {
	t.Parallel()
	tarball := []byte("tar")
	cases := map[string]struct {
		path, contentType string
		body              []byte
		prepare           func(t *testing.T, e deployEnv)
	}{
		"into a revision": {archivePath(revisionName), "application/x-tar", tarball, func(t *testing.T, e deployEnv) {
			status, out := e.createRevision(t, revisionName, bundleRevision())
			mustStatus(t, status, http.StatusCreated, "create a revision", out)
		}},
		"into a runner":           {"", "application/x-tar", tarball, nil},
		"at another path":         {deployVersion + "/containers/" + loaderName + "/archive?path=/etc", "application/x-tar", tarball, nil},
		"with no path":            {deployVersion + "/containers/" + loaderName + "/archive", "application/x-tar", tarball, nil},
		"at the root":             {deployVersion + "/containers/" + loaderName + "/archive?path=/", "application/x-tar", tarball, nil},
		"with a second parameter": {archivePath(loaderName) + "&copyUIDGID=1", "application/x-tar", tarball, nil},
		"as JSON":                 {archivePath(loaderName), "application/json", tarball, nil},
		"empty":                   {archivePath(loaderName), "application/x-tar", nil, nil},
		"over the bound":          {archivePath(loaderName), "application/x-tar", bytes.Repeat([]byte("x"), 2048), nil},
		"into a running loader": {archivePath(factory + "-loader-" + strings.Repeat("c", 12)), "application/x-tar", tarball, func(t *testing.T, e deployEnv) {
			e.daemon.AddContainer(factory+"-loader-"+strings.Repeat("c", 12), map[string]string{
				"sisf.factory": factory, "sisf.loader": "1", "sisf.bundle": "sha256:" + strings.Repeat("c", 64)})
		}},
		"into another factory's loader": {archivePath("other-loader-" + strings.Repeat("c", 12)), "application/x-tar", tarball, func(t *testing.T, e deployEnv) {
			e.daemon.AddContainer("other-loader-"+strings.Repeat("c", 12), map[string]string{
				"sisf.factory": "other", "sisf.loader": "1", "sisf.bundle": "sha256:" + strings.Repeat("c", 64)})
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newBundleEnv(t, func(c *dockerfilter.Config) { c.MaxArchiveBytes = 1024 })
			e.loaded(t)
			if tc.prepare != nil {
				tc.prepare(t, e)
			}
			path := tc.path
			if path == "" {
				path = archivePath(e.runner)
			}
			status, out := e.upload(t, path, tc.contentType, tc.body)
			if status < 400 {
				t.Fatalf("status %d (%s)", status, out)
			}
			for _, r := range e.daemon.Requests() {
				if r.Method == http.MethodPut && len(r.Body) > 0 {
					t.Fatalf("the refused upload reached the daemon: %s %s", r.Path, r.Query)
				}
			}
		})
	}
}

func TestTheRunnerRoleServesNoBundleEndpoint(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for name, req := range map[string]struct{ method, path, body string }{
		"an upload":         {http.MethodPut, version + "/containers/" + e.control + "/archive?path=/sisf-bundle", "tar"},
		"a volume create":   {http.MethodPost, version + "/volumes/create", `{"Name":"acme-app-bundle-bbbbbbbbbbbb"}`},
		"a volume inspect":  {http.MethodGet, version + "/volumes/acme-app-bundle-bbbbbbbbbbbb", ""},
		"a volume removal":  {http.MethodDelete, version + "/volumes/acme-app-bundle-bbbbbbbbbbbb", ""},
		"a list of volumes": {http.MethodGet, version + "/volumes", ""},
	} {
		before := len(e.daemon.Requests())
		status, out := e.do(t, req.method, req.path, req.body)
		if status != http.StatusForbidden {
			t.Errorf("%s: status %d (%s)", name, status, strings.TrimSpace(out))
		}
		if len(e.daemon.Requests()) != before {
			t.Errorf("%s reached the daemon", name)
		}
	}
}

// Every mount, user and environment a revision does not need is refused
// before the daemon sees it.
func TestARevisionMountsOnlyItsBundleAndItsDataVolumes(t *testing.T) {
	t.Parallel()
	mount := func(source, target string, readOnly bool) map[string]any {
		return map[string]any{"Type": "volume", "Source": source, "Target": target, "ReadOnly": readOnly}
	}
	setMounts := func(b map[string]any, mounts ...any) { host(b)["Mounts"] = mounts }
	otherBundle := factory + "-bundle-" + strings.Repeat("c", 12)
	cases := map[string]func(b map[string]any){
		"two bundles": func(b map[string]any) {
			setMounts(b, mount(bundleVolume, bundleTarget, true), mount(otherBundle, "/srv/other", true))
		},
		"a writable bundle":          func(b map[string]any) { setMounts(b, mount(bundleVolume, bundleTarget, false)) },
		"a bundle at another target": func(b map[string]any) { setMounts(b, mount(bundleVolume, "/srv", true)) },
		"another factory's bundle": func(b map[string]any) {
			setMounts(b, mount("other-bundle-"+strings.Repeat("b", 12), bundleTarget, true))
		},
		"an undeclared volume":           func(b map[string]any) { setMounts(b, mount("acme-app-secrets", "/run/secrets", true)) },
		"a data volume at another place": func(b map[string]any) { setMounts(b, mount(uploadsName, "/etc", false)) },
		"a data volume over the bundle":  func(b map[string]any) { setMounts(b, mount(uploadsName, bundleTarget, false)) },
		"a bind mount": func(b map[string]any) {
			setMounts(b, map[string]any{"Type": "bind", "Source": "/", "Target": "/host"})
		},
		"a tmpfs mount": func(b map[string]any) {
			setMounts(b, map[string]any{"Type": "tmpfs", "Target": "/scratch"})
		},
		"a volume backed by a host path": func(b map[string]any) {
			setMounts(b, map[string]any{"Type": "volume", "Source": uploadsName, "Target": uploadsAt,
				"VolumeOptions": map[string]any{"DriverConfig": map[string]any{"Name": "local",
					"Options": map[string]any{"type": "none", "o": "bind", "device": "/"}}}})
		},
		"bind options": func(b map[string]any) {
			setMounts(b, map[string]any{"Type": "volume", "Source": uploadsName, "Target": uploadsAt,
				"BindOptions": map[string]any{"Propagation": "rshared"}})
		},
		"Binds":             func(b map[string]any) { host(b)["Binds"] = []string{"/:/host"} },
		"anonymous Volumes": func(b map[string]any) { b["Volumes"] = map[string]any{"/data": map[string]any{}} },
		"a uid alone":       func(b map[string]any) { b["User"] = "33" },
		"root":              func(b map[string]any) { b["User"] = "0:0" },
		"the root group":    func(b map[string]any) { b["User"] = "33:0" },
		"a user name":       func(b map[string]any) { b["User"] = "www-data" },
		"no runtime environment": func(b map[string]any) {
			b["Env"] = []string{"SISF_REVISION=" + digest}
		},
		"an extra variable": func(b map[string]any) {
			b["Env"] = append(b["Env"].([]string), "LD_PRELOAD=/tmp/x.so")
		},
		"a changed variable": func(b map[string]any) {
			b["Env"] = []string{"SISF_REVISION=" + digest, "APACHE_RUN_DIR=/var/run", "WORDPRESS_DB_HOST=db"}
		},
		"a command": func(b map[string]any) { b["Cmd"] = []string{"true"} },
		"a loader label": func(b map[string]any) {
			b["Labels"].(map[string]any)["sisf.loader"] = "1"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newBundleEnv(t, nil)
			body := bundleRevision()
			mutate(body)
			status, out := e.createRevision(t, revisionName, body)
			mustStatus(t, status, http.StatusForbidden, name, out)
			if len(e.daemon.Created()) != 0 {
				t.Fatal("the refused create reached the daemon")
			}
		})
	}
	t.Run("a bundle where the filter allows none", func(t *testing.T) {
		t.Parallel()
		e := newBundleEnv(t, func(c *dockerfilter.Config) { c.BundleTarget = "" })
		status, out := e.createRevision(t, revisionName, bundleRevision())
		mustStatus(t, status, http.StatusForbidden, "a bundle without a bundle target", out)
		status, out = e.createRevision(t, loaderName, loaderCreate())
		mustStatus(t, status, http.StatusForbidden, "a loader without a bundle target", out)
		status, out = e.do(t, http.MethodPost, deployVersion+"/volumes/create", encode(t, volumeCreate()))
		mustStatus(t, status, http.StatusForbidden, "a bundle volume without a bundle target", out)
	})
}

func TestALoaderOnlyHoldsItsBundleVolumeOpen(t *testing.T) {
	t.Parallel()
	hostMounts := func(b map[string]any, mounts ...any) { host(b)["Mounts"] = mounts }
	loaderMount := func(source, target string, readOnly bool) map[string]any {
		return map[string]any{"Type": "volume", "Source": source, "Target": target, "ReadOnly": readOnly}
	}
	cases := map[string]func(b map[string]any) string{
		"a command":          func(b map[string]any) string { b["Cmd"] = []string{"sh", "-c", "id"}; return loaderName },
		"an environment":     func(b map[string]any) string { b["Env"] = []string{"A=b"}; return loaderName },
		"the deploy network": func(b map[string]any) string { host(b)["NetworkMode"] = deployNetwork; return loaderName },
		"a restart policy": func(b map[string]any) string {
			host(b)["RestartPolicy"] = map[string]any{"Name": "unless-stopped"}
			return loaderName
		},
		"another image": func(b map[string]any) string {
			b["Image"] = "alpine@sha256:" + strings.Repeat("e", 64)
			return loaderName
		},
		"a root user":         func(b map[string]any) string { b["User"] = "0:0"; return loaderName },
		"a writable root":     func(b map[string]any) string { host(b)["ReadonlyRootfs"] = false; return loaderName },
		"no limits":           func(b map[string]any) string { delete(host(b), "Memory"); return loaderName },
		"another name":        func(map[string]any) string { return factory + "-loader-" + strings.Repeat("c", 12) },
		"a revision name":     func(map[string]any) string { return revisionName },
		"a unit label":        func(b map[string]any) string { b["Labels"].(map[string]any)["sisf.unit"] = "app"; return loaderName },
		"a loader label of 0": func(b map[string]any) string { b["Labels"].(map[string]any)["sisf.loader"] = "0"; return loaderName },
		"another factory": func(b map[string]any) string {
			b["Labels"].(map[string]any)["sisf.factory"] = "other"
			return loaderName
		},
		"a read-only bundle": func(b map[string]any) string {
			hostMounts(b, loaderMount(bundleVolume, dockerfilter.DeployLoaderTarget, true))
			return loaderName
		},
		"another bundle's volume": func(b map[string]any) string {
			hostMounts(b, loaderMount(factory+"-bundle-"+strings.Repeat("c", 12), dockerfilter.DeployLoaderTarget, false))
			return loaderName
		},
		"the bundle at the revision's target": func(b map[string]any) string {
			hostMounts(b, loaderMount(bundleVolume, bundleTarget, false))
			return loaderName
		},
		"a data volume as well": func(b map[string]any) string {
			hostMounts(b, loaderMount(bundleVolume, dockerfilter.DeployLoaderTarget, false), loaderMount(uploadsName, uploadsAt, false))
			return loaderName
		},
		"a writable data volume instead": func(b map[string]any) string {
			hostMounts(b, loaderMount(uploadsName, dockerfilter.DeployLoaderTarget, false))
			return loaderName
		},
		"no mount": func(b map[string]any) string { delete(host(b), "Mounts"); return loaderName },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newBundleEnv(t, nil)
			body := loaderCreate()
			target := mutate(body)
			status, out := e.createRevision(t, target, body)
			mustStatus(t, status, http.StatusForbidden, name, out)
			if len(e.daemon.Created()) != 0 {
				t.Fatal("the refused create reached the daemon")
			}
		})
	}
}

func TestOnlyBundleVolumesOfThisFactoryAreMadeAndRemoved(t *testing.T) {
	t.Parallel()
	creates := map[string]func(b map[string]any){
		"a host path in disguise": func(b map[string]any) {
			b["Driver"] = "local"
			b["DriverOpts"] = map[string]any{"type": "none", "o": "bind", "device": "/"}
		},
		"a driver":              func(b map[string]any) { b["Driver"] = "local" },
		"a data volume's name":  func(b map[string]any) { b["Name"] = uploadsName },
		"another factory's":     func(b map[string]any) { b["Name"] = "other-bundle-" + strings.Repeat("b", 12) },
		"a name of another hex": func(b map[string]any) { b["Name"] = factory + "-bundle-" + strings.Repeat("c", 12) },
		"no bundle label":       func(b map[string]any) { delete(b["Labels"].(map[string]any), "sisf.bundle") },
		"an extra label":        func(b map[string]any) { b["Labels"].(map[string]any)["traefik.enable"] = "true" },
		"another factory label": func(b map[string]any) { b["Labels"].(map[string]any)["sisf.factory"] = "other" },
		"a short bundle": func(b map[string]any) {
			b["Labels"].(map[string]any)["sisf.bundle"] = "sha256:" + strings.Repeat("b", 12)
		},
	}
	for name, mutate := range creates {
		t.Run("create "+name, func(t *testing.T) {
			t.Parallel()
			e := newBundleEnv(t, nil)
			body := volumeCreate()
			mutate(body)
			status, out := e.do(t, http.MethodPost, deployVersion+"/volumes/create", encode(t, body))
			mustStatus(t, status, http.StatusForbidden, name, out)
			if len(e.daemon.Volumes()) != 0 || len(e.daemon.Requests()) != 0 {
				t.Fatal("the refused create reached the daemon")
			}
		})
	}
	e := newBundleEnv(t, nil)
	e.daemon.AddVolume(uploadsName, nil)
	foreign := factory + "-bundle-" + strings.Repeat("c", 12)
	e.daemon.AddVolume(foreign, map[string]string{"sisf.factory": "other"})
	for name, req := range map[string]struct{ method, path string }{
		"a data volume":                 {http.MethodDelete, "/volumes/" + uploadsName},
		"a bundle-named foreign volume": {http.MethodDelete, "/volumes/" + foreign},
		"a prune":                       {http.MethodPost, "/volumes/prune"},
	} {
		status, out := e.do(t, req.method, deployVersion+req.path, "")
		if status != http.StatusForbidden {
			t.Errorf("remove %s: status %d (%s)", name, status, strings.TrimSpace(out))
		}
	}
	if len(e.daemon.Volumes()) != 2 {
		t.Fatalf("a refused removal reached the daemon: %v", e.daemon.Volumes())
	}
	status, out := e.do(t, http.MethodGet, deployVersion+"/volumes/"+uploadsName, "")
	mustStatus(t, status, http.StatusForbidden, "inspect a data volume", out)
}

func TestADeployFilterChecksItsBundleSettings(t *testing.T) {
	t.Parallel()
	transport := testClient.Transport
	deployCfg := func(tweak func(*dockerfilter.Config)) dockerfilter.Config {
		c := dockerfilter.Config{Role: dockerfilter.RoleDeploy, Upstream: transport, RunnerValue: factory,
			Images: []string{unitImage}, DeployNetwork: deployNetwork}
		tweak(&c)
		return c
	}
	for name, cfg := range map[string]dockerfilter.Config{
		"a runner with a bundle target": {Upstream: transport, RunnerValue: factory, Images: []string{image}, SandboxNetwork: network, BundleTarget: bundleTarget},
		"a runner with data volumes": {Upstream: transport, RunnerValue: factory, Images: []string{image}, SandboxNetwork: network,
			DataVolumes: []dockerfilter.DeployVolume{{Name: uploadsName, Target: uploadsAt}}},
		"a runner with deploy env":       {Upstream: transport, RunnerValue: factory, Images: []string{image}, SandboxNetwork: network, DeployEnv: runtimeEnv},
		"a runner with an archive limit": {Upstream: transport, RunnerValue: factory, Images: []string{image}, SandboxNetwork: network, MaxArchiveBytes: 1},
		"a relative bundle target":       deployCfg(func(c *dockerfilter.Config) { c.BundleTarget = "var/www" }),
		"the root as bundle target":      deployCfg(func(c *dockerfilter.Config) { c.BundleTarget = "/" }),
		"an unclean bundle target":       deployCfg(func(c *dockerfilter.Config) { c.BundleTarget = "/var/../etc" }),
		"the loader's target":            deployCfg(func(c *dockerfilter.Config) { c.BundleTarget = dockerfilter.DeployLoaderTarget }),
		"a data volume named like a bundle": deployCfg(func(c *dockerfilter.Config) {
			c.DataVolumes = []dockerfilter.DeployVolume{{Name: factory + "-bundle-" + strings.Repeat("a", 12), Target: "/data"}}
		}),
		"a data volume at the bundle target": deployCfg(func(c *dockerfilter.Config) {
			c.BundleTarget = bundleTarget
			c.DataVolumes = []dockerfilter.DeployVolume{{Name: uploadsName, Target: bundleTarget}}
		}),
		"a data volume with a relative target": deployCfg(func(c *dockerfilter.Config) {
			c.DataVolumes = []dockerfilter.DeployVolume{{Name: uploadsName, Target: "uploads"}}
		}),
		"a data volume with a path as name": deployCfg(func(c *dockerfilter.Config) {
			c.DataVolumes = []dockerfilter.DeployVolume{{Name: "/var/lib", Target: "/data"}}
		}),
		"an env setting the revision": deployCfg(func(c *dockerfilter.Config) { c.DeployEnv = []string{"SISF_REVISION=x"} }),
		"an env setting docker":       deployCfg(func(c *dockerfilter.Config) { c.DeployEnv = []string{"DOCKER_HOST=tcp://x"} }),
		"an env without a value":      deployCfg(func(c *dockerfilter.Config) { c.DeployEnv = []string{"NAME"} }),
		"a negative archive limit":    deployCfg(func(c *dockerfilter.Config) { c.MaxArchiveBytes = -1 }),
	} {
		if _, err := dockerfilter.New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// putStream uploads body through the filter with no declared length.
func (e deployEnv) putStream(t *testing.T, path string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, e.filter.URL+path, io.MultiReader(bytes.NewReader(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// An upload of undeclared length is cut one byte past the bound, so the
// daemon never receives the whole of an oversized stream.
func TestAStreamedUploadIsCutAtItsBound(t *testing.T) {
	t.Parallel()
	e := newBundleEnv(t, func(c *dockerfilter.Config) { c.MaxArchiveBytes = 1024 })
	e.loaded(t)
	oversized := bytes.Repeat([]byte("x"), 64<<10)
	mustStatus(t, e.putStream(t, archivePath(loaderName), oversized), http.StatusRequestEntityTooLarge, "a streamed upload over the bound", "")
	for _, r := range e.daemon.Requests() {
		if r.Method == http.MethodPut && len(r.Body) > 1024 {
			t.Fatalf("the daemon received %d bytes of an upload bounded to 1024", len(r.Body))
		}
	}
	mustStatus(t, e.putStream(t, archivePath(loaderName), oversized[:1024]), http.StatusOK, "a streamed upload within the bound", "")
}
