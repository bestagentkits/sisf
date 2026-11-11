package conformance

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// A finding names the value it judges where that helps, such as an image or
// a route. A credential pasted into such a field must not be printed with
// it: wherever a value has the shape of a secret, a finding withholds it.
func TestAFindingNeverRepeatsAValueShapedLikeASecret(t *testing.T) {
	t.Parallel()
	const canary = "canary-opaque-value"
	credentialed := "https://ci:" + canary + "@git.example.com/acme/x"
	token := "ghp_" + strings.Repeat("Q", 36)
	cases := map[string]struct {
		valid  func(*testing.T) *manifest.Manifest
		change func(m *manifest.Manifest)
	}{
		"an image": {docker, func(m *manifest.Manifest) {
			editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Image = credentialed })
		}},
		"the supervisor's image": {docker, func(m *manifest.Manifest) {
			m.Supervisor.Image = "ghcr.io/" + token + "/sisf-supervisor"
		}},
		"a digest": {docker, func(m *manifest.Manifest) { m.Supervisor.Digest = core.Digest(credentialed) }},
		"a way out of a segment": {docker, func(m *manifest.Manifest) {
			network := m.Networks[manifest.NetworkSandbox]
			network.EgressVia = credentialed
			m.Networks[manifest.NetworkSandbox] = network
		}},
		"a public route": {docker, func(m *manifest.Manifest) {
			editUnit(m, manifest.UnitControl, func(u *manifest.Unit) { u.Public, u.PublicRoutes = true, []string{"/x?token=" + canary} })
		}},
		"a store role": {docker, func(m *manifest.Manifest) {
			m.Components.Agent.StoreRole = "postgres://u:" + canary + "@h/db"
			m.Components.Control.StoreRole = "postgres://u:" + canary + "@h/db"
		}},
		"a backend name": {docker, func(m *manifest.Manifest) { m.Adapters.Coder.Name = credentialed }},
		"a mounted volume's name": {docker, func(m *manifest.Manifest) {
			editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Mounts[0].Volume = "Bearer " + token })
		}},
		"a unit in release.units": {docker, func(m *manifest.Manifest) {
			m.Supervisor = nil
			m.Release.Units = []manifest.UnitName{manifest.UnitName(credentialed)}
		}},
		"the factory identifier": {docker, func(m *manifest.Manifest) { m.FactoryID = core.FactoryID(credentialed) }},
		"a key of adapter config": {docker, func(m *manifest.Manifest) {
			m.Adapters.Coder.Config = map[string]any{credentialed: "x"}
		}},
		"the first word of a command": {standalone, func(m *manifest.Manifest) {
			editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Command = []string{"--token=" + canary} })
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := tc.valid(t)
			tc.change(m)
			report := Check(m)
			if report.Conformant() {
				t.Fatalf("the change must be refused, got %+v", report)
			}
			for _, finding := range report.Findings {
				text := finding.Path + " " + finding.Message + " " + finding.Fix
				if strings.Contains(text, canary) || strings.Contains(text, token) {
					t.Errorf("finding [%s] repeats a value shaped like a secret: %+v", finding.Rule, finding)
				}
			}
			if err := report.Err(); strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), token) {
				t.Errorf("the error repeats a value shaped like a secret: %v", err)
			}
		})
	}
}

func TestWithholdingKeepsWhatHoldsNoSecret(t *testing.T) {
	t.Parallel()
	for _, text := range []string{`unit "app" runs image "ghcr.io/acme/app:v1"`, "set units.app.command to bin/app", "units.app.mounts[0].path"} {
		if got := withheldText(text); got != text {
			t.Errorf("withheldText(%q) = %q, want it unchanged", text, got)
		}
	}
	if got := withheldText(`fix "https://u:p@h/x" now`); strings.Contains(got, "u:p@") || !strings.Contains(got, "fix") {
		t.Errorf("only the secret is withheld, got %q", got)
	}
}
