package install

import (
	"os"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

func TestTheDoctorFailsAFakeIdentityProviderOutsideTheSimulation(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	o := initOptions(in, manifest.ModeDocker)
	production(&o)
	if _, err := in.Init(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	m, err := in.Layout.LoadManifest()
	must(t, err)
	var healthy collector
	in.checkIdentity(&healthy, m)
	for _, c := range healthy.report.Checks {
		if c.Verdict != VerdictOK {
			t.Errorf("a healthy production install: %s %s %s", c.Name, c.Verdict, c.Detail)
		}
	}
	m.Extensions[extOIDCIssuer] = "http://127.0.0.1:7468"
	var tampered collector
	in.checkIdentity(&tampered, m)
	if v := verdictOf(tampered.report, "fake identity"); len(v) != 1 || v[0] != VerdictFail {
		t.Errorf("fake identity = %v, want a failure", v)
	}
	delete(m.Extensions, extOIDCIssuer)
	delete(m.Extensions, extOIDCClientID)
	var none collector
	in.checkIdentity(&none, m)
	if v := verdictOf(none.report, "sign-in"); len(v) != 1 || v[0] != VerdictFail {
		t.Errorf("sign-in without a provider = %v, want a failure", v)
	}
}

func TestTheDoctorFailsWhenControlHoldsTheEdgeAdminTokenOrTheTokensMatch(t *testing.T) {
	t.Parallel()
	for name, damage := range map[string]func(l Layout){
		"control holds the admin token": func(l Layout) {
			appendEnv(t, l.RuntimeEnv(areaControl), "EDGE_ADMIN_TOKEN=x")
		},
		"control holds the admin url": func(l Layout) {
			appendEnv(t, l.RuntimeEnv(areaControl), "SISF_EDGE_ADMIN_URL=http://edge-admin:7469")
		},
		"the counters token is the admin token": func(l Layout) {
			edge, _ := readEnvFile(l.RuntimeEnv(areaEdge))
			data, _ := formatEnv("EDGE_ADMIN_TOKEN", edge["EDGE_ADMIN_TOKEN"], "EDGE_COUNTERS_TOKEN", edge["EDGE_ADMIN_TOKEN"])
			must(t, writeSecret(l.RuntimeEnv(areaEdge), data))
		},
		"control's counters token is not the edge's": func(l Layout) {
			setEnvForTest(t, l.RuntimeEnv(areaControl), "EDGE_COUNTERS_TOKEN", strings.Repeat("c", 43))
		},
	} {
		in, _, _ := newTestInstaller(t)
		mustInit(t, in, manifest.ModeDocker)
		damage(in.Layout)
		var c collector
		in.checkEdgeTokens(&c)
		if v := verdictOf(c.report, "edge tokens"); len(v) == 0 || v[0] != VerdictFail {
			t.Errorf("%s: edge tokens = %v, want a failure", name, v)
		}
	}
}

// appendEnv adds a line to an env file the installer wrote.
func appendEnv(t *testing.T, path, line string) {
	t.Helper()
	data, err := os.ReadFile(path)
	must(t, err)
	must(t, writeSecret(path, append(data, []byte(line+"\n")...)))
}

// setEnvForTest replaces one variable of an env file.
func setEnvForTest(t *testing.T, path, key, value string) {
	t.Helper()
	must(t, setEnvValue(path, key, value))
}
