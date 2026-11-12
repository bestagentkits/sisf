package install

import (
	"os"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

func TestTheSecretsCheckCatchesSharedAndMisplacedSecrets(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		rule   string
		damage func(t *testing.T, l Layout)
	}{
		"two zones share a credential": {ruleCredentials, func(t *testing.T, l Layout) {
			data, _ := os.ReadFile(l.Secret(areaControl, "credentials"))
			must(t, writeSecret(l.Secret(areaApplication, "credentials"), data))
		}},
		"the qa credential equals the runner's": {ruleCredentials, func(t *testing.T, l Layout) {
			data, _ := os.ReadFile(l.Secret(areaAgent, "credentials"))
			must(t, writeSecret(l.Secret(areaAgent, "qa-credentials"), data))
		}},
		"zone 1 holds the counters token": {ruleZoneKeys, func(t *testing.T, l Layout) {
			env, _ := readEnvFile(l.RuntimeEnv(areaApplication))
			data, _ := formatEnv("SISF_ZONE", "application", "SISF_STATE_DSN", env["SISF_STATE_DSN"], "EDGE_COUNTERS_TOKEN", "leaked")
			must(t, writeSecret(l.RuntimeEnv(areaApplication), data))
		}},
		"zone 2 opens the store as the control role": {ruleCredentials, func(t *testing.T, l Layout) {
			env, _ := readEnvFile(l.RuntimeEnv(areaControl))
			data, _ := formatEnv("SISF_ZONE", "agent", "SISF_STATE_DSN", env["SISF_STATE_DSN"])
			must(t, writeSecret(l.RuntimeEnv(areaAgent), data))
		}},
		"the root key sits in the agent area": {ruleZoneKeys, func(t *testing.T, l Layout) {
			must(t, writeSecret(l.Secret(areaAgent, "root-key.pem"), []byte("x")))
		}},
		"a secret is world readable": {rulePermissions, func(t *testing.T, l Layout) {
			must(t, os.Chmod(l.Secret(areaControl, "webhook-secret"), 0o644))
		}},
	} {
		in, _, _ := newTestInstaller(t)
		mustInit(t, in, manifest.ModeDocker)
		tc.damage(t, in.Layout)
		vs, err := CheckSecrets(in.Layout, manifest.ModeDocker)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		found := false
		for _, v := range vs {
			found = found || v.Rule == tc.rule
		}
		if !found {
			t.Errorf("%s: want a %s violation, got %v", name, tc.rule, vs)
		}
	}
}

func TestEachZoneHasItsOwnStoreRoleAndCredential(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeDocker)
	m, _ := in.Layout.LoadManifest()
	seen := map[string]core.Zone{}
	for _, z := range core.Zones() {
		c := m.Components.Zone(z)
		if other, dup := seen[c.StoreRole]; dup {
			t.Errorf("%s and %s share store role %s", z, other, c.StoreRole)
		}
		seen[c.StoreRole] = z
		if c.StoreRole != zoneRoles[z] {
			t.Errorf("%s role %q differs from the one bootstrap creates, %q", z, c.StoreRole, zoneRoles[z])
		}
	}
}
