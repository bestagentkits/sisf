package install

import (
	"testing"

	"github.com/bestagentkits/sisf/internal/conformance"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

func testOptions(mode manifest.Mode) Options {
	return Options{Dir: "/tmp/sisf-test", Mode: mode, Repo: manifest.Repository{Owner: "acme", Repo: "hello-app"}, Sim: true}
}

func TestGeneratedManifestPassesTheConformanceCheckerInBothModes(t *testing.T) {
	t.Parallel()
	for _, mode := range []manifest.Mode{manifest.ModeStandalone, manifest.ModeDocker} {
		o, err := testOptions(mode).normalize()
		if err != nil {
			t.Fatal(err)
		}
		report := conformance.Check(buildManifest(o))
		if err := report.Err(); err != nil {
			t.Errorf("%s manifest is not conformant: %v\n%+v", mode, err, report.Findings)
		}
		if mode == manifest.ModeDocker && report.TrustedCodeOnly {
			t.Error("the Docker manifest relies on trusted_code_only; it isolates runs and must not")
		}
		if mode == manifest.ModeStandalone && !report.TrustedCodeOnly {
			t.Error("the Standalone manifest does not say it is for trusted code only")
		}
		if !report.SelfUpdate {
			t.Errorf("%s manifest does not keep the supervisor boundary", mode)
		}
	}
}

func TestGeneratedManifestValidatesAgainstTheSchema(t *testing.T) {
	t.Parallel()
	for _, mode := range []manifest.Mode{manifest.ModeStandalone, manifest.ModeDocker} {
		o, _ := testOptions(mode).normalize()
		if violations := manifest.Validate(buildManifest(o)); len(violations) != 0 {
			t.Errorf("%s manifest violates the schema: %v", mode, violations)
		}
	}
}

func TestGeneratedManifestHoldsReferencesAndNoSecretValues(t *testing.T) {
	t.Parallel()
	o, _ := testOptions(manifest.ModeDocker).normalize()
	m := buildManifest(o)
	refs := []string{m.Supervisor.RootKeyRef, m.Storage.State.DSNRef}
	for _, c := range []*manifest.Component{m.Components.Application, m.Components.Agent, m.Components.Control} {
		refs = append(refs, c.CredentialsRef)
	}
	for _, ref := range refs {
		if !manifest.IsSecretRef(ref) {
			t.Errorf("%q is not a secret reference", ref)
		}
	}
}

func TestUnconfirmedBudgetAlertsOnAnySpendingAndNeverSwitchesTheAlertOff(t *testing.T) {
	t.Parallel()
	o, _ := testOptions(manifest.ModeStandalone).normalize()
	b := buildManifest(o).Budget
	if b.Mode != "unlimited" || b.AlertOff || b.AlertThresholdMicros != unconfirmedAlertMicros {
		t.Fatalf("unconfirmed budget = %+v, want unlimited with the smallest alert threshold", b)
	}
}

func TestNormalizeRejectsUnsafeOptions(t *testing.T) {
	t.Parallel()
	good := testOptions(manifest.ModeDocker)
	cases := map[string]func(*Options){
		"relative directory":     func(o *Options) { o.Dir = "install" },
		"unknown mode":           func(o *Options) { o.Mode = "swarm" },
		"debug without docker":   func(o *Options) { o.Mode, o.Debug = manifest.ModeStandalone, true },
		"repo with a slash":      func(o *Options) { o.Repo.Repo = "a/b" },
		"repo traversal":         func(o *Options) { o.Repo.Repo = ".." },
		"factory id with space":  func(o *Options) { o.FactoryID = "my factory" },
		"plain http to a host":   func(o *Options) { o.GitHubAPIURL = "http://github.example.com" },
		"credentials in the URL": func(o *Options) { o.GitHubAPIURL = "https://user:pw@api.github.com" },
		"alert without confirm":  func(o *Options) { o.Budget = BudgetChoice{AlertOff: true} },
		"confirm without alert":  func(o *Options) { o.Budget = BudgetChoice{Confirmed: true} },
	}
	for name, mutate := range cases {
		o := good
		mutate(&o)
		if _, err := o.normalize(); err == nil {
			t.Errorf("%s: options were accepted", name)
		}
	}
}

func TestNormalizeDerivesIdentityFromTheRepository(t *testing.T) {
	t.Parallel()
	o, err := testOptions(manifest.ModeDocker).normalize()
	if err != nil {
		t.Fatal(err)
	}
	if o.FactoryID != "sisf-hello-app" || o.DeploymentID != "local-docker" {
		t.Fatalf("identity = %s / %s", o.FactoryID, o.DeploymentID)
	}
	if got := project(o.FactoryID); got != "sisf-hello-app" {
		t.Fatalf("project = %q", got)
	}
	if got := project("acme.shop"); got != "sisf-acme-shop" {
		t.Fatalf("project for a dotted id = %q, want a Compose-safe name carrying the sisf- prefix", got)
	}
}
