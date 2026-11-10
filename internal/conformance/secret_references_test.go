package conformance

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Each reference in the manifest names a secret with a holder of its own.
// One reference in two places gives both holders the same secret.
func TestNoSecretReferenceStandsForTwoSecrets(t *testing.T) {
	t.Parallel()
	const shared = "local:hunter2"
	cases := []struct {
		name   string
		change func(m *manifest.Manifest)
		rule   Rule
		path   string
	}{
		{"Zone 1's credentials are the state connection secret",
			func(m *manifest.Manifest) { m.Components.Application.CredentialsRef = m.Storage.State.DSNRef },
			RuleSecretRefReused, "storage.state.dsn_ref"},
		{"Zone 2's credentials are the state connection secret",
			func(m *manifest.Manifest) { m.Components.Agent.CredentialsRef, m.Storage.State.DSNRef = shared, shared },
			RuleSecretRefReused, "storage.state.dsn_ref"},
		{"the root public key is found where Zone 2 keeps its credentials",
			func(m *manifest.Manifest) { m.Supervisor.RootKeyRef = m.Components.Agent.CredentialsRef },
			RuleSecretRefReused, "supervisor.root_key_ref"},
		{"the root public key is found where the qa-runner keeps its credentials",
			func(m *manifest.Manifest) { m.Supervisor.RootKeyRef = m.Units[manifest.UnitQARunner].CredentialsRef },
			RuleSecretRefReused, "supervisor.root_key_ref"},
		{"the root public key is the state connection secret",
			func(m *manifest.Manifest) { m.Supervisor.RootKeyRef = m.Storage.State.DSNRef },
			RuleSecretRefReused, "supervisor.root_key_ref"},
		{"one reference in another letter case",
			func(m *manifest.Manifest) { m.Supervisor.RootKeyRef = "local:Zone3-Control" },
			RuleSecretRefReused, "supervisor.root_key_ref"},
		{"a unit of Zone 2 uses the credentials of Zone 3",
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitQARunner, func(u *manifest.Unit) { u.CredentialsRef = m.Components.Control.CredentialsRef })
			}, RuleSharedCredentials, "units.qa-runner.credentials_ref"},
		{"the application unit uses the credentials of Zone 2",
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.CredentialsRef = m.Components.Agent.CredentialsRef })
			}, RuleSharedCredentials, "units.app.credentials_ref"},
		{"units of two zones name one credential set of their own",
			func(m *manifest.Manifest) {
				for _, unit := range []manifest.UnitName{manifest.UnitApp, manifest.UnitDispatcher} {
					editUnit(m, unit, func(u *manifest.Unit) { u.CredentialsRef = shared })
				}
			}, RuleSharedCredentials, "units.dispatcher.credentials_ref"},
		{"the edge, which every zone uses, runs with the credentials of Zone 3",
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitEdge, func(u *manifest.Unit) { u.CredentialsRef = m.Components.Control.CredentialsRef })
			}, RuleSharedCredentials, "units.edge.credentials_ref"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := docker(t)
			tc.change(m)
			report := Check(m)
			wantOnly(t, report, tc.rule, tc.path)
			if len(report.Findings) != 1 {
				t.Fatalf("one reused reference is one finding, got %+v", report.Findings)
			}
			for _, reference := range []string{"hunter2", "zone1-application", "zone2-agent", "zone2-qa", "zone3-control", "Zone3-Control", "SISF_STATE_DSN", "SISF_ROOT_PUBLIC_KEY"} {
				if finding := report.Findings[0]; strings.Contains(finding.Message+finding.Fix, reference) {
					t.Errorf("the finding repeats the reference %q: %+v", reference, finding)
				}
			}
		})
	}

	// Units of one zone may run with that zone's credential set.
	m := docker(t)
	editUnit(m, manifest.UnitDispatcher, func(u *manifest.Unit) { u.CredentialsRef = m.Components.Agent.CredentialsRef })
	editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.CredentialsRef = m.Components.Control.CredentialsRef })
	if report := Check(m); !report.Conformant() {
		t.Fatalf("a unit may name its own zone's credential set, got %+v", report.Findings)
	}
}

// A store that folds role names opens two spellings as one role.
func TestStoreRolesThatDifferOnlyByCaseAreOneRole(t *testing.T) {
	t.Parallel()
	for _, role := range []string{"SISF_Application", "SISF_APPLICATION", " sisf_application "} {
		for mode, valid := range bothModes {
			m := valid(t)
			m.Components.Agent.StoreRole = role
			report := Check(m)
			wantOnly(t, report, RuleSharedStoreRole, "components.agent.store_role")
			if len(report.Findings) != 1 {
				t.Fatalf("%s: one shared role is one finding, got %+v", mode, report.Findings)
			}
		}
	}
}
