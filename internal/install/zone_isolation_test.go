package install

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// Static half of the zone isolation guarantee: the rendered Compose project
// and the secrets of a fresh install are checked without starting Docker. The
// live half, which tries to cross the boundaries on real containers, is in
// zone_isolation_e2e_test.go under the e2e tag.

func TestRenderedComposeProjectBreaksNoIsolationRule(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*Options){
		"sim":   nil,
		"real":  func(o *Options) { o.Sim = false; o.Repo = manifest.Repository{Owner: "acme", Repo: "shop"} },
		"debug": func(o *Options) { o.Debug = true },
	} {
		in, out := renderCompose(t, mutate)
		vs, err := CheckCompose(in.Layout, out)
		if err != nil || len(vs) != 0 {
			t.Errorf("%s: violations %v (%v)\n%s", name, violationRules(vs), err, out)
		}
	}
}

func TestComposeHasOneServicePerUnitAndTheFourNetworks(t *testing.T) {
	t.Parallel()
	_, out := renderCompose(t, nil)
	var file composeFile
	if err := yaml.Unmarshal(out, &file); err != nil {
		t.Fatal(err)
	}
	for _, unit := range []string{"app", "control", "dispatcher", "supervisor", "state", "edge"} {
		if _, ok := file.Services[unit]; !ok {
			t.Errorf("no service for unit %s", unit)
		}
	}
	for _, n := range []string{"app", "factory", "control", "sandbox"} {
		if _, ok := file.Networks[n]; !ok {
			t.Errorf("network %s is missing", n)
		}
	}
	if _, ok := file.Volumes["artifacts"]; !ok {
		t.Error("the artifacts volume is missing")
	}
	if !file.Networks["sandbox"].Internal || !file.Networks["factory"].Internal {
		t.Error("the sandbox and factory networks must be internal")
	}
	if file.Services["app"].Networks[0] != "app" || len(file.Services["app"].Networks) != 1 {
		t.Errorf("Zone 1 networks = %v, want only app", file.Services["app"].Networks)
	}
	for _, n := range file.Services["state"].Networks {
		if n == "app" || n == "sandbox" {
			t.Errorf("the state store is reachable from the %s network", n)
		}
	}
}

func TestZoneOneHasNoPathIntoTheControlZone(t *testing.T) {
	t.Parallel()
	in, out := renderCompose(t, nil)
	app := composeFileOf(t, out).Services["app"]
	if len(app.EnvFile) != 1 || app.EnvFile[0] != in.Layout.RuntimeEnv(areaApplication) {
		t.Fatalf("app env files = %v, want its own only", app.EnvFile)
	}
	for _, n := range app.Networks {
		if n == "control" || n == "factory" {
			t.Fatalf("Zone 1 sits on the %s network", n)
		}
	}
	// And its credential opens no control role: the store role in its
	// env file is its own, with its own password.
	if v, err := CheckSecrets(in.Layout, manifest.ModeDocker); err != nil || len(v) != 0 {
		t.Fatalf("secret separation: %v %v", v, err)
	}
	g, err := sqlstore.GrantStatements("public", sqlstore.ZoneRoles(zoneRoles))
	if err != nil {
		t.Fatal(err)
	}
	granted := ""
	for _, s := range g {
		if strings.HasPrefix(s, "GRANT") && strings.HasSuffix(s, `TO "sisf_application"`) {
			granted += s + "\n"
		}
	}
	if strings.Count(granted, "GRANT SELECT ON TABLE") != 1 || !strings.Contains(granted, `"public"."schema_migrations"`) ||
		strings.Contains(granted, "INSERT") || strings.Contains(granted, "UPDATE") || strings.Contains(granted, "DELETE") {
		t.Fatalf("Zone 1's role is granted more than the schema version:\n%s", granted)
	}
}

func TestOnlyTheFiltersHaveDockerAccessAndTheStateStoreIsOutOfTheEdgesReach(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*Options){
		"sim":   nil,
		"debug": func(o *Options) { o.Debug = true },
	} {
		_, out := renderCompose(t, mutate)
		f := composeFileOf(t, out)
		for svc, s := range f.Services {
			filter := svc == filterRunner || svc == filterDeploy
			if filter && (!slices.Equal(s.Volumes, []string{socketMount}) || !slices.Equal([]string(s.Networks), []string{dockerNetwork})) {
				t.Errorf("%s: filter %s mounts %v on %v, want the socket read-only on the docker network alone", name, svc, s.Volumes, s.Networks)
			}
			for _, v := range s.Volumes {
				for _, sock := range dockerSocketNames {
					if strings.Contains(v, sock) && !filter {
						t.Errorf("%s: service %s mounts %s", name, svc, v)
					}
				}
			}
			if _, set := s.Environment["DOCKER_HOST"]; set {
				t.Errorf("%s: service %s sets DOCKER_HOST", name, svc)
			}
			if s.Privileged {
				t.Errorf("%s: service %s is privileged", name, svc)
			}
			for _, word := range socketProxyWords {
				if strings.Contains(svc, word) || strings.Contains(s.Image, word) {
					t.Errorf("%s: service %s (%s) is a socket proxy", name, svc, s.Image)
				}
			}
		}
		edge := f.Services["edge"].Networks
		for _, n := range f.Services["state"].Networks {
			if slices.Contains(edge, n) {
				t.Errorf("%s: edge and state share the %s network", name, n)
			}
		}
	}
}

func TestEveryServiceThatNeedsTheStateStoreReachesItOnTheFactoryNetwork(t *testing.T) {
	t.Parallel()
	_, out := renderCompose(t, nil)
	f := composeFileOf(t, out)
	if got := f.Services["state"].Networks; !slices.Equal(got, []string{"factory"}) {
		t.Fatalf("state networks = %v, want only factory", got)
	}
	for _, name := range []string{"control", "dispatcher", "state-bootstrap"} {
		if !slices.Contains(f.Services[name].Networks, "factory") {
			t.Errorf("%s cannot reach the state store: not on the factory network", name)
		}
	}
	if slices.Contains(f.Services["edge"].Networks, "factory") {
		t.Error("the edge is on the factory network")
	}
}

func TestEveryImageIsPinnedByDigestAndEveryNameCarriesThePrefix(t *testing.T) {
	t.Parallel()
	_, out := renderCompose(t, nil)
	f := composeFileOf(t, out)
	pin := regexp.MustCompile(`(@sha256:[0-9a-f]{64}|^sha256:[0-9a-f]{64})$`)
	for name, s := range f.Services {
		if !pin.MatchString(s.Image) {
			t.Errorf("service %s: image %q is not pinned by digest", name, s.Image)
		}
		if !strings.HasPrefix(s.ContainerName, "sisf-") {
			t.Errorf("service %s: container %q lacks the sisf- prefix", name, s.ContainerName)
		}
	}
	if !strings.Contains(f.Services["state"].Image, "@sha256:") {
		t.Error("a third-party image is not pinned by digest")
	}
	for n, v := range f.Networks {
		if !strings.HasPrefix(v.Name, "sisf-") {
			t.Errorf("network %s lacks the sisf- prefix", n)
		}
	}
	for n, v := range f.Volumes {
		if !strings.HasPrefix(v.Name, "sisf-") {
			t.Errorf("volume %s lacks the sisf- prefix", n)
		}
	}
}

// publishedOnLoopbackInRange matches host:container ports bound to 127.0.0.1
// with both ports in 7460-7469.
var publishedOnLoopbackInRange = regexp.MustCompile(`^127\.0\.0\.1:746[0-9]:746[0-9]$`)

func TestEveryPublishedPortIsOnLoopbackInTheFixedRange(t *testing.T) {
	t.Parallel()
	_, out := renderCompose(t, nil)
	f := composeFileOf(t, out)
	for name, s := range f.Services {
		for _, p := range s.Ports {
			if !publishedOnLoopbackInRange.MatchString(p) {
				t.Errorf("service %s publishes %q", name, p)
			}
		}
	}
	if got := f.Services["edge"].Ports; len(got) != 1 || got[0] != "127.0.0.1:7460:7460" {
		t.Errorf("edge ports = %v", got)
	}
}

func TestNothingMountsTheSecretsDirectoryAndSecretsStayOutOfTheComposeFile(t *testing.T) {
	t.Parallel()
	in, out := renderCompose(t, nil)
	f := composeFileOf(t, out)
	for name, s := range f.Services {
		for _, v := range s.Volumes {
			if strings.Contains(v, "/secrets") {
				t.Errorf("service %s mounts %s", name, v)
			}
		}
	}
	text := string(out)
	for _, area := range []string{areaControl, areaAgent, areaApplication} {
		data, _ := os.ReadFile(in.Layout.Secret(area, "credentials"))
		if strings.Contains(text, strings.TrimSpace(string(data))) {
			t.Errorf("the Compose file holds the %s credential", area)
		}
	}
}

func TestSimulationProfileRunsTheFakeGitHubInsideControlsNamespaceOnly(t *testing.T) {
	t.Parallel()
	_, sim := renderCompose(t, nil)
	f := composeFileOf(t, sim)
	s, ok := f.Services["sim-github"]
	if !ok || s.NetworkMode != "service:control" {
		t.Fatalf("sim-github = %+v", s)
	}
	_, real := renderCompose(t, func(o *Options) { o.Sim = false; o.Repo = manifest.Repository{Owner: "acme", Repo: "shop"} })
	if _, ok := composeFileOf(t, real).Services["sim-github"]; ok {
		t.Fatal("the fake GitHub is part of a project that is not the simulation profile")
	}
	if strings.Contains(string(real), "7467") {
		t.Fatal("port 7467 is published without the simulation profile")
	}
}

// The control zone reports the health of the other two zones, so it must
// reach Zone 1 through the edge and Zone 2 on a network both are on.
func TestControlProbesTheOtherZonesOnAddressesItCanReach(t *testing.T) {
	t.Parallel()
	_, out := renderCompose(t, nil)
	for _, want := range []string{
		`SISF_APP_HEALTH_URL: "http://edge:7460/healthz"`,
		`SISF_DISPATCHER_HEALTH_URL: "http://dispatcher:7465/healthz"`,
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the control service lacks %s", want)
		}
	}
}
