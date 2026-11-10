package manifest_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// asCoreError unwraps err into the typed error every action returns.
func asCoreError(err error, target **core.Error) bool { return errors.As(err, target) }

func TestValidExamplesHaveNoSchemaViolation(t *testing.T) {
	for _, path := range []string{examplePath, "../../internal/conformance/testdata/valid-standalone.yaml"} {
		m, err := manifest.ParseFile(path)
		if err != nil {
			t.Fatalf("%s must parse: %v", path, err)
		}
		if got := manifest.Validate(m); len(got) != 0 {
			t.Errorf("%s must match the schema, got %v", path, got)
		}
	}
}

func TestValidateLocatesEachDepartureFromTheSchema(t *testing.T) {
	cases := map[string]struct {
		change func(m *manifest.Manifest)
		path   string
		says   string
	}{
		"a zone without a component":             {func(m *manifest.Manifest) { m.Components.Agent = nil }, "components.agent", "required"},
		"a unit that is not declared":            {func(m *manifest.Manifest) { delete(m.Units, manifest.UnitRunner) }, "units.runner", "required"},
		"a unit outside the nine":                {func(m *manifest.Manifest) { m.Units["worker"] = m.Units[manifest.UnitApp] }, "units.worker", "not an allowed key"},
		"a network segment that is not declared": {func(m *manifest.Manifest) { delete(m.Networks, manifest.NetworkSandbox) }, "networks.sandbox", "required"},
		"another manifest version":               {func(m *manifest.Manifest) { m.APIVersion = "sisf/v2" }, "api_version", "sisf/v1"},
		"a factory id that is not label safe":    {func(m *manifest.Manifest) { m.FactoryID = "Acme App" }, "factory_id", "pattern"},
		"an operating mode that does not exist":  {func(m *manifest.Manifest) { m.Mode = "kubernetes" }, "mode", "standalone"},
		"a port outside the range": {func(m *manifest.Manifest) {
			u := m.Units[manifest.UnitApp]
			u.Port = 70000
			m.Units[manifest.UnitApp] = u
		}, "units.app.port", "maximum"},
		"a mount without a path":                          {func(m *manifest.Manifest) { m.Units[manifest.UnitApp].Mounts[0].Path = "" }, "units.app.mounts[0].path", "minLength"},
		"a tag where a digest belongs":                    {func(m *manifest.Manifest) { m.Supervisor.Digest = "latest" }, "supervisor.digest", "pattern"},
		"a volume whose name is not label safe":           {func(m *manifest.Manifest) { m.Volumes["Trust Me"] = manifest.Volume{Zone: core.ZoneControl} }, "volumes.Trust Me", "not an allowed key"},
		"a volume owned by no zone":                       {func(m *manifest.Manifest) { m.Volumes["trust"] = manifest.Volume{Zone: "everyone"} }, "volumes.trust.zone", "control"},
		"a connection string written in the file":         {func(m *manifest.Manifest) { m.Storage.State.DSNRef = "postgres://db.internal:5432/sisf" }, "storage.state.dsn_ref", "pattern"},
		"a postgres store without a connection reference": {func(m *manifest.Manifest) { m.Storage.State.DSNRef = "" }, "storage.state.dsn_ref", "required"},
		"a sqlite store without a file":                   {func(m *manifest.Manifest) { m.Storage.State = manifest.StateStore{Driver: manifest.StateSQLite} }, "storage.state.path", "required"},
		"a capped budget without a cap":                   {func(m *manifest.Manifest) { m.Budget.Mode = core.BudgetCapped }, "budget.cap_micros", "required"},
		"a negative alert threshold":                      {func(m *manifest.Manifest) { m.Budget.AlertThresholdMicros = -1 }, "budget.alert_threshold_micros", "minimum"},
		"no attempts allowed":                             {func(m *manifest.Manifest) { m.Limits.MaxAttempts = 0 }, "limits.max_attempts", "minimum"},
		"a canary step above all traffic":                 {func(m *manifest.Manifest) { m.Release.CanarySteps = []int{5, 250} }, "release.canary_steps[1]", "maximum"},
		"an error rate that is not a share":               {func(m *manifest.Manifest) { m.Release.Rollback.ErrorRate = 2 }, "release.rollback.error_rate", "maximum"},
		"a delegation mode that does not exist":           {func(m *manifest.Manifest) { m.Release.Policy = "D" }, "release.policy", "value must be"},
		"an egress entry that allows everything":          {func(m *manifest.Manifest) { m.Egress.Allow = []string{"*"} }, "egress.allow[0]", "pattern"},
		"a model route without a model": {func(m *manifest.Manifest) {
			m.ModelRoutes.Routes[core.StepCoding] = core.ModelRoute{Provider: "anthropic"}
		}, "model_routes.routes.coding.model", "minLength"},
		"a step kind that does not exist": {func(m *manifest.Manifest) {
			m.ModelRoutes.Routes["dreaming"] = core.ModelRoute{Provider: "p", Model: "m"}
		}, "model_routes.routes.dreaming", "not an allowed key"},
		"an adapter without a name": {func(m *manifest.Manifest) { m.Adapters.Deploy.Name = "" }, "adapters.deploy.name", "pattern"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := example(t)
			tc.change(m)
			got := manifest.Validate(m)
			for _, v := range got {
				if v.Path == tc.path && strings.Contains(v.Message, tc.says) {
					return
				}
			}
			t.Fatalf("want a violation at %s saying %q, got %v", tc.path, tc.says, got)
		})
	}
}

func TestValidateRefusesWhatHasNoJSONForm(t *testing.T) {
	if got := manifest.Validate(nil); len(got) != 1 {
		t.Fatalf("no manifest is one violation, got %v", got)
	}
	m := example(t)
	m.Extensions = map[string]any{"x-team": map[any]any{1: "pager"}}
	got := manifest.Validate(m)
	if len(got) != 1 || got[0].Path != "" || !strings.Contains(got[0].String(), "text keys") {
		t.Fatalf("a setting without a JSON form must be refused, got %v", got)
	}
}
