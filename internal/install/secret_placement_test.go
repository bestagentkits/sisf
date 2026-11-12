package install

import (
	"os"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// The supervisor releases: it holds the GitHub token, the control role's
// store connection, the approval public key and the two admin tokens. The
// agent zone holds none of them but the token of its own run control route.
func TestTheSupervisorHoldsWhatReleasingNeedsAndTheAgentZoneNoneOfIt(t *testing.T) {
	t.Parallel()
	for _, mode := range []manifest.Mode{manifest.ModeDocker, manifest.ModeStandalone} {
		in, _, _ := newTestInstaller(t)
		mustInit(t, in, mode)
		sup, err := readEnvFile(in.Layout.Secret(areaControl, "supervisor.env"))
		must(t, err)
		control, err := readEnvFile(in.Layout.RuntimeEnv(areaControl))
		must(t, err)
		agent, err := readEnvFile(in.Layout.RuntimeEnv(areaAgent))
		must(t, err)
		edge, err := readEnvFile(in.Layout.RuntimeEnv(areaEdge))
		must(t, err)
		for _, key := range []string{"SISF_ROOT_PUBLIC_KEY", "SISF_STATE_DSN", "SISF_GITHUB_TOKEN", "SISF_APPROVAL_PUBLIC_KEY",
			"EDGE_ADMIN_TOKEN", "SISF_DISPATCHER_ADMIN_TOKEN"} {
			if sup[key] == "" {
				t.Errorf("%s: the supervisor lacks %s", mode, key)
			}
		}
		if sup["SISF_GITHUB_TOKEN"] != control["SISF_GITHUB_TOKEN"] || sup["SISF_STATE_DSN"] != control["SISF_STATE_DSN"] {
			t.Errorf("%s: the supervisor's GitHub token or store connection is not the control zone's", mode)
		}
		for _, key := range []string{"SISF_GITHUB_TOKEN", "EDGE_ADMIN_TOKEN", "EDGE_COUNTERS_TOKEN", "SISF_APPROVAL_KEY",
			"SISF_AUTH_OIDC_CLIENT_SECRET", "SISF_CONTROL_SECRET_X"} {
			if _, held := agent[key]; held {
				t.Errorf("%s: the agent zone holds %s", mode, key)
			}
		}
		if agent["SISF_DISPATCHER_ADMIN_TOKEN"] != sup["SISF_DISPATCHER_ADMIN_TOKEN"] || len(agent["SISF_DISPATCHER_ADMIN_TOKEN"]) < 32 {
			t.Errorf("%s: the dispatcher's run control token is not the one the supervisor calls with", mode)
		}
		if len(edge) != 2 || edge["EDGE_ADMIN_TOKEN"] != sup["EDGE_ADMIN_TOKEN"] || len(edge["EDGE_ADMIN_TOKEN"]) < 32 {
			t.Errorf("%s: the edge's env file is not its admin and counters tokens alone", mode)
		}
		// Control reads the edge's counters with the read-only token the edge
		// checks, which is not the admin token; it holds no admin token.
		if control["EDGE_COUNTERS_TOKEN"] != edge["EDGE_COUNTERS_TOKEN"] || len(control["EDGE_COUNTERS_TOKEN"]) < 32 ||
			control["EDGE_COUNTERS_TOKEN"] == edge["EDGE_ADMIN_TOKEN"] {
			t.Errorf("%s: control's counters token is not the edge's read-only token", mode)
		}
		for _, key := range []string{"EDGE_ADMIN_TOKEN", "SISF_EDGE_ADMIN_URL", "SISF_OWNER_TOKEN"} {
			if _, held := control[key]; held {
				t.Errorf("%s: control holds %s", mode, key)
			}
		}
		if sup["EDGE_ADMIN_TOKEN"] == sup["SISF_DISPATCHER_ADMIN_TOKEN"] {
			t.Errorf("%s: the two admin tokens are the same", mode)
		}
		if vs, err := CheckSecrets(in.Layout, mode); err != nil || len(vs) != 0 {
			t.Errorf("%s: a fresh install breaks the secret rules: %v %v", mode, vs, err)
		}
	}
}

// The approval public key the supervisor verifies with is the public half
// of the seed control signs with, as factoryd derives it.
func TestTheSupervisorsApprovalKeyIsThePublicHalfOfControlsSeed(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeDocker)
	sup, _ := readEnvFile(in.Layout.Secret(areaControl, "supervisor.env"))
	control, _ := readEnvFile(in.Layout.RuntimeEnv(areaControl))
	priv, err := wiring.ParseApprovalKey(core.NewSecretValue([]byte(control["SISF_APPROVAL_KEY"])))
	must(t, err)
	pub, err := signing.ParsePublicKey(sup["SISF_APPROVAL_PUBLIC_KEY"])
	must(t, err)
	if !pub.Equal(priv.Public()) {
		t.Fatal("the supervisor verifies approvals with another key than control signs them with")
	}
	if _, err := approvalPublicKey("not a seed"); err == nil {
		t.Fatal("a malformed approval seed was accepted")
	}
}

// Each widening of where a secret is kept is refused by the secret rules.
func TestTheSecretRulesRefuseEveryWideningOfTheNewSecrets(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ file, line string }{
		"the agent zone holds the GitHub token":       {"agent/runtime.env", "SISF_GITHUB_TOKEN=x"},
		"the agent zone holds the edge admin token":   {"agent/runtime.env", "EDGE_ADMIN_TOKEN=x"},
		"the application holds the run control token": {"application/runtime.env", "SISF_DISPATCHER_ADMIN_TOKEN=x"},
		"the edge holds the GitHub token":             {"edge/runtime.env", "SISF_GITHUB_TOKEN=x"},
		"the edge holds the run control token":        {"edge/runtime.env", "SISF_DISPATCHER_ADMIN_TOKEN=x"},
		"control holds the edge admin token":          {"control/runtime.env", "EDGE_ADMIN_TOKEN=x"},
		"control holds the edge admin url":            {"control/runtime.env", "SISF_EDGE_ADMIN_URL=x"},
		"the supervisor holds the counters token":     {"control/supervisor.env", "EDGE_COUNTERS_TOKEN=x"},
		"the supervisor holds an OIDC client secret":  {"control/supervisor.env", "SISF_AUTH_OIDC_CLIENT_SECRET=x"},
		"the agent zone holds an OIDC client secret":  {"agent/runtime.env", "SISF_AUTH_OIDC_CLIENT_SECRET=x"},
		"the agent zone holds a control secret":       {"agent/runtime.env", "SISF_CONTROL_SECRET_SYSTEM_ONE=x"},
		"the supervisor holds a control secret":       {"control/supervisor.env", "SISF_CONTROL_SECRET_SYSTEM_ONE=x"},
		"the edge holds a control secret":             {"edge/runtime.env", "SISF_CONTROL_SECRET_SYSTEM_ONE=x"},
		"the edge holds the github login secret":      {"edge/runtime.env", "SISF_AUTH_GITHUB_CLIENT_SECRET=x"},
		"the supervisor holds the approval seed":      {"control/supervisor.env", "SISF_APPROVAL_KEY=x"},
		"the supervisor holds the store owner":        {"control/supervisor.env", "SISF_STATE_OWNER_DSN=x"},
	} {
		in, _, _ := newTestInstaller(t)
		mustInit(t, in, manifest.ModeDocker)
		path := in.Layout.Dir + "/secrets/" + tc.file
		data, err := os.ReadFile(path)
		must(t, err)
		must(t, os.WriteFile(path, append(data, []byte(tc.line+"\n")...), secretFileMode))
		vs, err := CheckSecrets(in.Layout, manifest.ModeDocker)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(violationRules(vs), ruleZoneKeys) {
			t.Errorf("%s: not refused, got [%s]", name, violationRules(vs))
		}
	}
	// The supervisor opening the store as another zone's role is refused too.
	in, _, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeDocker)
	path := in.Layout.Secret(areaControl, "supervisor.env")
	sup, _ := readEnvFile(path)
	agent, _ := readEnvFile(in.Layout.RuntimeEnv(areaAgent))
	sup["SISF_STATE_DSN"] = agent["SISF_STATE_DSN"]
	var pairs []string
	for _, k := range sortedKeys(sup) {
		pairs = append(pairs, k, sup[k])
	}
	data, err := formatEnv(pairs...)
	must(t, err)
	must(t, os.WriteFile(path, data, secretFileMode))
	if vs, _ := CheckSecrets(in.Layout, manifest.ModeDocker); !strings.Contains(violationRules(vs), ruleCredentials) {
		t.Errorf("the supervisor opening the store as the agent role was not refused: [%s]", violationRules(vs))
	}
}
