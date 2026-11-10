package conformance

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

func TestFactoryDeploymentAndRuntimeIdentifiersCanBeToldApart(t *testing.T) {
	cases := []struct {
		name       string
		factory    core.FactoryID
		deployment core.DeploymentID
		rule       Rule
		path       string
	}{
		{"the installation repeats the factory's name", "acme-hello", "acme-hello", RuleIdentityNotDistinct, "deployment_id"},
		{"the factory is named like a runtime unit", "runner", "local", RuleIdentityNotDistinct, "factory_id"},
		{"the installation is named like a runtime unit", "acme-hello", "app", RuleIdentityNotDistinct, "deployment_id"},
		{"the factory has no identifier", "", "local", RuleIdentityInvalid, "factory_id"},
		{"the installation has no identifier", "acme-hello", "", RuleIdentityInvalid, "deployment_id"},
		{"the factory identifier is not label safe", "Acme Hello", "local", RuleIdentityInvalid, "factory_id"},
		{"the installation identifier is a path", "acme-hello", "../prod", RuleIdentityInvalid, "deployment_id"},
		{"the factory identifier is too long", core.FactoryID(strings.Repeat("a", 64)), "local", RuleIdentityInvalid, "factory_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := docker(t)
			m.FactoryID, m.DeploymentID = tc.factory, tc.deployment
			wantOnly(t, Check(m), tc.rule, tc.path)
		})
	}
}

func TestEveryRuntimeUnitNameIsRefusedAsAnIdentity(t *testing.T) {
	for _, name := range manifest.UnitNames() {
		m := docker(t)
		m.FactoryID = core.FactoryID(name)
		wantOnly(t, Check(m), RuleIdentityNotDistinct, "factory_id")
	}
}

// Two installations of one factory share the factory_id and differ in the
// deployment_id; both are conformant.
func TestTwoInstallationsOfOneFactoryDifferOnlyInDeploymentID(t *testing.T) {
	local, staging := docker(t), docker(t)
	staging.DeploymentID = "staging"
	if local.FactoryID != staging.FactoryID || local.DeploymentID == staging.DeploymentID {
		t.Fatal("the two installations must share a factory and differ in deployment")
	}
	for _, m := range []*manifest.Manifest{local, staging} {
		if report := Check(m); !report.Conformant() {
			t.Fatalf("installation %q must be conformant, got %+v", m.DeploymentID, report.Findings)
		}
	}
}
