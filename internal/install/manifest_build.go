package install

import (
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Profile values of extensions.profile.
const (
	profileSim     = "sim"
	profileDefault = "default"
)

// rootKeyRef is where the supervisor finds the root public key.
const rootKeyRef = "env:SISF_ROOT_PUBLIC_KEY"

// buildManifest declares one factory deployment for the options. Every
// secret is a reference, every artifact digest starts as a placeholder until
// `sisf up` builds and pins it, and the result is meant to pass the
// conformance checker as it stands.
func buildManifest(o Options) *manifest.Manifest {
	docker := o.Mode == manifest.ModeDocker
	m := &manifest.Manifest{
		APIVersion: manifest.APIVersion, FactoryID: o.FactoryID, DeploymentID: o.DeploymentID,
		Mode: o.Mode, Repository: o.Repo,
		Components: manifest.Components{
			Application: component("apps/web", areaApplication, "sisf_application"),
			Agent:       component("factory", areaAgent, "sisf_agent"),
			Control:     component("control", areaControl, "sisf_control"),
		},
		Units: units(o.Mode),
		Supervisor: &manifest.Supervisor{
			Image: supervisorImage(o.Mode), Digest: unbuilt(artifactSupervisor), TrustVolume: "trust", RootKeyRef: rootKeyRef,
			DigestChange: manifest.DigestChangeGovernance, StartupSelfCheck: true,
		},
		Networks: map[manifest.NetworkName]manifest.Network{
			manifest.NetworkApp:     {},
			manifest.NetworkFactory: {Internal: docker},
			manifest.NetworkControl: {},
			manifest.NetworkSandbox: {Internal: true, EgressVia: string(manifest.UnitEdge)},
		},
		Volumes: volumes(),
		Storage: manifest.Storage{
			State:     manifest.StateStore{Driver: manifest.StateSQLite, Path: "data/state/factory.db"},
			Artifacts: manifest.ArtifactStore{Kind: manifest.ArtifactsVolume, Location: "artifacts"},
		},
		Adapters: manifest.Adapters{
			Coder: manifest.AdapterRef{Name: "claude"}, QA: manifest.AdapterRef{Name: "codex"},
			Execution: manifest.AdapterRef{Name: "local"}, Deploy: manifest.AdapterRef{Name: "local"},
		},
		Release: release(docker),
		Budget:  budget(o.Budget),
		Limits: manifest.Limits{
			RunTimeout: manifest.Duration(30 * minute), MaxAttempts: 3, Concurrency: 2,
			ClarifyTimeout: manifest.Duration(72 * hour),
		},
		Egress:      egressPolicy(),
		ModelRoutes: modelRoutes(),
		Extensions:  map[string]any{"github_api_url": o.GitHubAPIURL, "profile": profile(o.Sim)},
	}
	if docker {
		m.Storage.State = manifest.StateStore{Driver: manifest.StatePostgres, DSNRef: "env:SISF_STATE_DSN"}
		m.Adapters.Execution, m.Adapters.Deploy = manifest.AdapterRef{Name: "docker"}, manifest.AdapterRef{Name: "compose"}
	} else {
		// Local processes are neither isolated nor held to the egress list,
		// so the Owner states that only trusted code runs here.
		m.TrustedCodeOnly = true
	}
	return m
}

func profile(sim bool) string {
	if sim {
		return profileSim
	}
	return profileDefault
}

// component declares one zone. The credentials and the store role are the
// zone's own, so no two zones share either.
func component(source, area, role string) *manifest.Component {
	return &manifest.Component{
		Source: source, CredentialsRef: "local:" + area + "/credentials", StoreRole: role, HealthCheck: "/healthz",
	}
}

func volumes() map[string]manifest.Volume {
	return map[string]manifest.Volume{
		"app-data":      {Zone: core.ZoneApplication},
		"workspaces":    {Zone: core.ZoneAgent},
		"qa-workspaces": {Zone: core.ZoneAgent},
		"control-data":  {Zone: core.ZoneControl},
		"trust":         {Zone: core.ZoneControl},
		"state-data":    {Zone: core.ZoneShared},
		"artifacts":     {Zone: core.ZoneShared},
	}
}

func release(docker bool) manifest.Release {
	r := manifest.Release{
		Policy: core.DelegationProposal, Strategy: manifest.StrategyBlueGreen,
		Units: []manifest.UnitName{manifest.UnitApp},
		Rollback: manifest.Rollback{
			ErrorRate: 0.02, BaselineDelta: 0.01, MinSamples: 100,
			Window: manifest.Duration(10 * minute), ConsecutiveWindows: 2,
		},
		LowTraffic: manifest.LowTraffic{MinHold: manifest.Duration(30 * minute), SyntheticSuite: "synthetic"},
	}
	if docker {
		r.Strategy, r.CanarySteps = manifest.StrategyCanary, []int{5, 25, 100}
	}
	return r
}

// unconfirmedAlertMicros is the alert threshold of a budget the Owner has
// not confirmed yet: the smallest amount, so that any spending at all is
// flagged. The manifest must state an alert decision to be conformant, and
// that decision is not the Owner's until they confirm; the factory claims
// nothing in the meantime because no confirmation exists in the store.
const unconfirmedAlertMicros core.Micros = 1

// budget records the Owner's choice.
func budget(c BudgetChoice) manifest.Budget {
	b := manifest.Budget{
		Mode: core.BudgetUnlimited, Period: manifest.PeriodMonthly,
		AlertThresholdMicros: c.AlertThresholdMicros, AlertOff: c.AlertOff,
	}
	if !c.Confirmed {
		b.AlertThresholdMicros = unconfirmedAlertMicros
	}
	return b
}

// egressPolicy is the default allowlist of §7: the model providers, GitHub,
// package and image registries, and operating-system repositories.
func egressPolicy() core.EgressPolicy {
	return core.EgressPolicy{Mode: core.EgressAllowlist, Allow: []string{
		"api.anthropic.com", "api.openai.com", "github.com", "api.github.com", "*.githubusercontent.com",
		"registry.npmjs.org", "pypi.org", "files.pythonhosted.org", "crates.io", "static.crates.io",
		"proxy.golang.org", "sum.golang.org", "repo.maven.apache.org", "registry-1.docker.io",
		"auth.docker.io", "ghcr.io", "deb.debian.org", "archive.ubuntu.com", "dl-cdn.alpinelinux.org",
	}}
}

// modelRoutes names tiers, not products; the Owner maps them to models.
func modelRoutes() manifest.ModelRoutes {
	return manifest.ModelRoutes{
		Routes: map[core.StepKind]core.ModelRoute{
			core.StepIntake:   {Provider: "anthropic", Model: "small", Effort: "low"},
			core.StepCoding:   {Provider: "anthropic", Model: "mid", Effort: "low"},
			core.StepPlanning: {Provider: "anthropic", Model: "large", Effort: "medium"},
			core.StepReadOnly: {Provider: "anthropic", Model: "small"},
			core.StepQA:       {Provider: "openai", Model: "mid", Effort: "low"},
		},
		Escalation: map[core.StepKind][]core.ModelRoute{
			core.StepCoding: {{Provider: "anthropic", Model: "mid", Effort: "high"}, {Provider: "anthropic", Model: "large", Effort: "low"}},
		},
		MaxEscalations: 2,
	}
}

// supervisorImage is the supervisor's own artifact in the mode: an image in
// Docker, the program file in Standalone.
func supervisorImage(mode manifest.Mode) string {
	if mode == manifest.ModeDocker {
		return "sisf-supervisor"
	}
	return "bin/sisf-supervisor"
}
