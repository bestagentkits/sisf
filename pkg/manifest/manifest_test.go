package manifest_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

func TestThereAreNineUnitsEachPlacedInItsZone(t *testing.T) {
	want := map[manifest.UnitName]core.Zone{
		manifest.UnitApp:        core.ZoneApplication,
		manifest.UnitControl:    core.ZoneControl,
		manifest.UnitSupervisor: core.ZoneControl,
		manifest.UnitDispatcher: core.ZoneAgent,
		manifest.UnitRunner:     core.ZoneAgent,
		manifest.UnitQARunner:   core.ZoneAgent,
		manifest.UnitState:      core.ZoneShared,
		manifest.UnitArtifacts:  core.ZoneShared,
		manifest.UnitEdge:       core.ZoneShared,
	}
	units := manifest.UnitNames()
	if len(units) != 9 || len(want) != 9 {
		t.Fatalf("UnitNames() = %v", units)
	}
	for _, u := range units {
		if !u.Valid() || u.Zone() != want[u] {
			t.Errorf("unit %q: valid=%v zone=%q, want %q", u, u.Valid(), u.Zone(), want[u])
		}
	}
	if string(manifest.UnitQARunner) != "qa-runner" {
		t.Errorf("unit names are the same on every platform, got %q", manifest.UnitQARunner)
	}
	if manifest.UnitName("worker").Valid() || manifest.UnitName("worker").Zone().Valid() {
		t.Error("an unknown unit is invalid and belongs to no zone")
	}
}

func TestEphemeralUnitsAreTheTwoRunners(t *testing.T) {
	want := map[manifest.UnitName]manifest.Lifecycle{
		manifest.UnitRunner: manifest.LifecycleEphemeral, manifest.UnitQARunner: manifest.LifecycleEphemeral,
		manifest.UnitState: manifest.LifecycleDurable, manifest.UnitArtifacts: manifest.LifecycleDurable,
	}
	for _, u := range manifest.UnitNames() {
		expected := manifest.LifecycleLongRunning
		if l, ok := want[u]; ok {
			expected = l
		}
		if u.Lifecycle() != expected || !u.Lifecycle().Valid() {
			t.Errorf("unit %q lifecycle = %q, want %q", u, u.Lifecycle(), expected)
		}
	}
	if manifest.UnitName("worker").Lifecycle().Valid() {
		t.Error("an unknown unit has no lifecycle")
	}
}

func TestThereAreFourNetworkSegments(t *testing.T) {
	want := []manifest.NetworkName{manifest.NetworkApp, manifest.NetworkFactory, manifest.NetworkControl, manifest.NetworkSandbox}
	if got := manifest.NetworkNames(); !slices.Equal(got, want) {
		t.Fatalf("NetworkNames() = %v", got)
	}
	if !manifest.NetworkSandbox.Valid() || manifest.NetworkName("public").Valid() {
		t.Error("network validation is wrong")
	}
}

func TestClosedSettingsRejectUnknownValues(t *testing.T) {
	valid := []interface{ Valid() bool }{
		manifest.ModeStandalone, manifest.ModeDocker, manifest.StateSQLite, manifest.StatePostgres,
		manifest.ArtifactsVolume, manifest.ArtifactsObjectStore, manifest.StrategyBlueGreen, manifest.StrategyCanary,
		manifest.DigestChangeGovernance, manifest.DigestChangePipeline, manifest.PeriodOneTime, manifest.PeriodMonthly,
	}
	for _, v := range valid {
		if !v.Valid() {
			t.Errorf("%v must be valid", v)
		}
	}
	invalid := []interface{ Valid() bool }{
		manifest.Mode(""), manifest.Mode("serverless"), manifest.StateDriver("mysql"), manifest.ArtifactsKind(""),
		manifest.Strategy("big_bang"), manifest.DigestChange(""), manifest.BudgetPeriod("yearly"), manifest.Lifecycle(""),
	}
	for _, v := range invalid {
		if v.Valid() {
			t.Errorf("%q must be invalid", v)
		}
	}
}

func TestOnlyAGovernanceCycleKeepsTheSupervisorOutOfThePipelinesReach(t *testing.T) {
	if !manifest.DigestChangeGovernance.HumanApproved() {
		t.Error("a governance cycle is approved by a person")
	}
	if manifest.DigestChangePipeline.HumanApproved() || manifest.DigestChange("").HumanApproved() {
		t.Error("the release pipeline, or an undeclared rule, must not count as human approval")
	}
}

func TestComponentsAreAddressedByZoneAndAMissingOneIsReported(t *testing.T) {
	app, control := &manifest.Component{Source: "apps/web"}, &manifest.Component{Source: "control"}
	c := manifest.Components{Application: app, Control: control}
	if c.Zone(core.ZoneApplication) != app || c.Zone(core.ZoneControl) != control {
		t.Fatal("a component must be found by its zone")
	}
	if c.Zone(core.ZoneAgent) != nil || c.Zone(core.ZoneShared) != nil || c.Zone("other") != nil {
		t.Fatal("a zone without a component must yield nil")
	}
	if got := c.Missing(); !slices.Equal(got, []core.Zone{core.ZoneAgent}) {
		t.Fatalf("Missing() = %v", got)
	}
	c.Agent = &manifest.Component{Source: "factory"}
	if len(c.Missing()) != 0 {
		t.Fatal("no zone is missing when all three components are declared")
	}
}

func TestDurationIsWrittenAsText(t *testing.T) {
	var cfg struct {
		Window manifest.Duration `json:"window"`
	}
	if err := json.Unmarshal([]byte(`{"window":"10m"}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Window.Std() != 10*time.Minute {
		t.Fatalf("window = %v", cfg.Window.Std())
	}
	out, err := json.Marshal(cfg)
	if err != nil || string(out) != `{"window":"10m0s"}` {
		t.Fatalf("Marshal = %s, %v", out, err)
	}
	for _, bad := range []string{`{"window":"soon"}`, `{"window":"-5m"}`, `{"window":""}`, `{"window":600}`} {
		if err := json.Unmarshal([]byte(bad), &cfg); err == nil {
			t.Errorf("%s must be rejected", bad)
		}
	}
}

const sample = `{
  "api_version": "sisf/v1", "factory_id": "acme-app", "deployment_id": "local", "mode": "docker",
  "repository": {"owner": "acme", "repo": "app"},
  "components": {
    "application": {"source": "apps/web", "credentials_ref": "zone1", "store_role": "app"},
    "agent": {"source": "factory", "credentials_ref": "zone2", "store_role": "factory"},
    "control": {"source": "control", "credentials_ref": "zone3", "store_role": "control"}
  },
  "units": {
    "app": {"image": "ghcr.io/acme/app", "lifecycle": "long_running", "networks": ["app"], "port": 8080, "public": true},
    "runner": {"lifecycle": "ephemeral", "networks": ["sandbox"], "mounts": [{"volume": "workspaces", "path": "/work"}]}
  },
  "supervisor": {"image": "ghcr.io/acme/supervisor", "digest": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
    "trust_volume": "trust", "root_key_ref": "env:SISF_ROOT_KEY", "digest_change": "governance", "startup_self_check": true},
  "networks": {"sandbox": {"internal": true, "egress_via": "egress-proxy"}},
  "volumes": {"trust": {"zone": "control"}},
  "storage": {"state": {"driver": "postgres", "dsn_ref": "env:SISF_DSN"}, "artifacts": {"kind": "volume", "location": "artifacts"}},
  "adapters": {"coder": {"name": "claude-cli"}, "qa": {"name": "codex"}, "execution": {"name": "docker"}, "deploy": {"name": "compose"}},
  "release": {"policy": "B", "strategy": "canary", "canary_steps": [5, 25, 100], "units": ["app"],
    "rollback": {"error_rate": 0.02, "baseline_delta": 0.01, "min_samples": 100, "window": "10m", "consecutive_windows": 2},
    "low_traffic": {"min_hold": "30m", "synthetic_suite": "tests/synthetic"}},
  "budget": {"mode": "unlimited", "alert_threshold_micros": 50000000, "period": "monthly"},
  "limits": {"run_timeout": "30m", "max_attempts": 3, "concurrency": 2, "clarify_timeout": "72h"},
  "egress": {"mode": "allowlist", "allow": ["api.github.com"]},
  "model_routes": {"routes": {"coding": {"provider": "anthropic", "model": "mid", "effort": "low"}},
    "escalation": {"coding": [{"provider": "anthropic", "model": "large", "effort": "low"}]}, "max_escalations": 2},
  "extensions": {"x-team": {"pager": "ops"}}
}`

func TestManifestDecodesFromItsDocumentedKeys(t *testing.T) {
	var m manifest.Manifest
	dec := json.NewDecoder(strings.NewReader(sample))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("the sample must decode without unknown keys: %v", err)
	}
	checks := map[string]bool{
		"api version":       m.APIVersion == manifest.APIVersion,
		"factory id":        m.FactoryID == "acme-app" && m.DeploymentID == "local",
		"mode":              m.Mode == manifest.ModeDocker,
		"repository":        m.Repository.Owner == "acme" && m.Repository.Repo == "app",
		"three components":  len(m.Components.Missing()) == 0 && m.Components.Agent.StoreRole == "factory",
		"credentials":       m.Components.Control.CredentialsRef == "zone3",
		"unit":              m.Units[manifest.UnitApp].Public && m.Units[manifest.UnitApp].Port == 8080,
		"unit mount":        m.Units[manifest.UnitRunner].Mounts[0].Volume == "workspaces",
		"supervisor":        m.Supervisor != nil && m.Supervisor.StartupSelfCheck && m.Supervisor.DigestChange.HumanApproved() && m.Supervisor.TrustVolume == "trust",
		"network":           m.Networks[manifest.NetworkSandbox].Internal && m.Networks[manifest.NetworkSandbox].EgressVia == "egress-proxy",
		"volume":            m.Volumes["trust"].Zone == core.ZoneControl,
		"storage":           m.Storage.State.Driver == manifest.StatePostgres && m.Storage.Artifacts.Kind == manifest.ArtifactsVolume,
		"adapters":          m.Adapters.Coder.Name == "claude-cli" && m.Adapters.QA.Name == "codex",
		"release":           m.Release.Policy == core.DelegationProposal && m.Release.Strategy == manifest.StrategyCanary && slices.Equal(m.Release.CanarySteps, []int{5, 25, 100}),
		"rollback":          m.Release.Rollback.Window.Std() == 10*time.Minute && m.Release.Rollback.MinSamples == 100 && m.Release.Rollback.ConsecutiveWindows == 2,
		"low traffic":       m.Release.LowTraffic.MinHold.Std() == 30*time.Minute && m.Release.LowTraffic.SyntheticSuite != "",
		"budget":            m.Budget.Mode == core.BudgetUnlimited && m.Budget.AlertThresholdMicros == 50_000_000 && m.Budget.Period == manifest.PeriodMonthly,
		"limits":            m.Limits.RunTimeout.Std() == 30*time.Minute && m.Limits.MaxAttempts == 3 && m.Limits.Concurrency == 2,
		"egress":            m.Egress.Mode == core.EgressAllowlist && m.Egress.Validate() == nil,
		"model routes":      m.ModelRoutes.Routes[core.StepCoding].Model == "mid" && len(m.ModelRoutes.Escalation[core.StepCoding]) == 1,
		"escalation cap":    m.ModelRoutes.MaxEscalations == 2,
		"extensions":        m.Extensions["x-team"] != nil,
		"release unit list": slices.Equal(m.Release.Units, []manifest.UnitName{manifest.UnitApp}),
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("%s was not decoded as documented", name)
		}
	}
}

// The same key must name a field in YAML and in JSON, so the file on disk and
// the published schema cannot drift apart.
func TestEveryManifestFieldHasTheSameKeyInYAMLAndJSON(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Map:
			walk(typ.Elem())
			return
		case reflect.Struct:
		default:
			return
		}
		if seen[typ] || !strings.HasSuffix(typ.PkgPath(), "pkg/manifest") {
			return
		}
		seen[typ] = true
		for i := range typ.NumField() {
			f := typ.Field(i)
			jsonKey, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			yamlKey, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			if jsonKey == "" || jsonKey != yamlKey {
				t.Errorf("%s.%s: json key %q, yaml key %q", typ.Name(), f.Name, jsonKey, yamlKey)
			}
			walk(f.Type)
		}
	}
	walk(reflect.TypeOf(manifest.Manifest{}))
	if len(seen) < 15 {
		t.Fatalf("only %d manifest types were checked", len(seen))
	}
}
