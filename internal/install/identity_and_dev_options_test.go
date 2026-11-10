package install

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// initErr runs Init with the options of a simulated install edited by
// mutate and returns its error.
func initErr(t *testing.T, mode manifest.Mode, mutate func(*Options)) error {
	t.Helper()
	in, _, _ := newTestInstaller(t)
	o := initOptions(in, mode)
	mutate(&o)
	_, err := in.Init(t.Context(), o)
	return err
}

func TestInitRefusesSignInSettingsThatAreMissingOrPointAtTheFake(t *testing.T) {
	t.Parallel()
	fake := "http://127.0.0.1:7468"
	for name, tc := range map[string]struct {
		mutate func(*Options)
		says   string
	}{
		"no provider outside the simulation": {func(o *Options) { production(o); o.Identity.OIDCIssuer, o.Identity.OIDCClientID = "", "" },
			"needs a sign-in provider"},
		"no Owner outside the simulation": {func(o *Options) { production(o); o.Identity.Owners = nil }, "--owner"},
		"the issuer is the fake": {func(o *Options) { production(o); o.Identity.OIDCIssuer = fake },
			"fake identity provider"},
		"the issuer is the fake on localhost": {func(o *Options) { production(o); o.Identity.OIDCIssuer = "http://localhost:7468" },
			"fake identity provider"},
		"the GitHub login is the fake": {func(o *Options) {
			production(o)
			o.Identity.GitHubClientID, o.Identity.GitHubClientSecret = "gh", core.NewSecretValue([]byte("gh-secret"))
			o.Identity.GitHubURL = fake
		}, "fake identity provider"},
		"the Owner signs in at the fake": {func(o *Options) { production(o); o.Identity.Owners = []string{fake + "#owner"} },
			"fake identity provider"},
		"the issuer is plain http": {func(o *Options) { production(o); o.Identity.OIDCIssuer = "http://accounts.example.com" },
			"https"},
		"GitHub sign-in without its secret": {func(o *Options) { production(o); o.Identity.GitHubClientID = "gh" },
			"client secret"},
		"the external URL is public http": {func(o *Options) { production(o); o.Identity.ExternalURL = "http://factory.example.com" },
			"https"},
		"the external URL has a path": {func(o *Options) { production(o); o.Identity.ExternalURL = "https://factory.example.com/x" },
			"origin"},
		"the Owner is not issuer#subject": {func(o *Options) { production(o); o.Identity.Owners = []string{"owner"} },
			"issuer#subject"},
		"the simulation is given a provider": {func(o *Options) { o.Identity.OIDCIssuer = "https://accounts.example.com" },
			"fake identity provider"},
		"the simulation is reached off loopback": {func(o *Options) { o.Identity.ExternalURL = "https://factory.example.com" },
			"loopback"},
	} {
		err := initErr(t, manifest.ModeDocker, tc.mutate)
		if !core.HasCode(err, core.CodeInvalidArgument) || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%s: Init = %v, want INVALID_ARGUMENT naming %q", name, err, tc.says)
		}
	}
}

func TestAProductionInstallKeepsTheClientSecretInControlsEnvFileOnly(t *testing.T) {
	t.Parallel()
	in, _, out := newTestInstaller(t)
	o := initOptions(in, manifest.ModeDocker)
	production(&o)
	res, err := in.Init(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	secret := string(o.Identity.OIDCClientSecret.Reveal())
	control, err := readEnvFile(in.Layout.RuntimeEnv(areaControl))
	must(t, err)
	if control["SISF_AUTH_OIDC_CLIENT_SECRET"] != secret {
		t.Fatal("control's env file lacks the OIDC client secret")
	}
	manifestText, _ := os.ReadFile(in.Layout.Config())
	for where, text := range map[string]string{"output": out.String() + sprintf("%+v", res), "factory.yaml": string(manifestText)} {
		if strings.Contains(text, secret) {
			t.Errorf("the %s shows the client secret", where)
		}
	}
	m, err := in.Layout.LoadManifest()
	must(t, err)
	id := identityOf(m)
	if id.ExternalURL != "https://factory.example.com" || id.OIDCIssuer != "https://accounts.example.com" ||
		len(id.Owners) != 1 || id.Owners[0] != "https://accounts.example.com#owner-1" || id.usesFake() {
		t.Fatalf("recorded sign-in settings = %+v", id)
	}
	for _, area := range []string{areaAgent, areaApplication, areaEdge} {
		if env, _ := readEnvFile(in.Layout.RuntimeEnv(area)); env["SISF_AUTH_OIDC_CLIENT_SECRET"] != "" {
			t.Errorf("the %s area holds the client secret", area)
		}
	}
}

func TestTheSimulationSignsInAtTheFakeAsTheDefaultOwner(t *testing.T) {
	t.Parallel()
	in, compose := renderCompose(t, nil)
	m, err := in.Layout.LoadManifest()
	must(t, err)
	id := identityOf(m)
	if !id.usesFake() || id.OIDCClientID != "sisf-local" || len(id.Owners) != 1 || id.Owners[0] != "http://127.0.0.1:7468#owner" ||
		id.ExternalURL != "http://127.0.0.1:7463" {
		t.Fatalf("simulated sign-in = %+v", id)
	}
	_, real := renderCompose(t, production)
	if _, ok := composeFileOf(t, real).Services["sim-oidc"]; ok || strings.Contains(string(real), "127.0.0.1:7468") {
		t.Error("a production Compose project runs or publishes the fake identity provider")
	}
	if s, ok := composeFileOf(t, compose).Services["sim-oidc"]; !ok || s.NetworkMode != "service:control" {
		t.Errorf("the simulation's fake identity provider = %+v", s)
	}
	for _, want := range []string{"127.0.0.1:7468:7468", `SISF_OWNER_IDENTITIES: "http://127.0.0.1:7468#owner"`} {
		if !strings.Contains(string(compose), want) {
			t.Errorf("the simulated Compose project lacks %s", want)
		}
	}
}

func TestTheDevelopmentProfileIsStandaloneOnLoopbackOnly(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		mode   manifest.Mode
		mutate func(*Options)
	}{
		"docker":               {manifest.ModeDocker, func(o *Options) { o.Sim, o.Dev = false, true }},
		"with the simulation":  {manifest.ModeStandalone, func(o *Options) { o.Dev = true }},
		"reached off loopback": {manifest.ModeStandalone, func(o *Options) { o.Sim, o.Dev, o.Identity.ExternalURL = false, true, "https://dev.example.com" }},
		"with a real provider": {manifest.ModeStandalone, func(o *Options) {
			o.Sim, o.Dev, o.Identity.OIDCIssuer, o.Identity.OIDCClientID = false, true, "https://accounts.example.com", "x"
		}},
		"gh credential without dev": {manifest.ModeStandalone, func(o *Options) { production(o); o.GitHubCredential = GitHubCredentialGHCLI }},
		"an unknown credential":     {manifest.ModeStandalone, func(o *Options) { o.Sim, o.Dev, o.GitHubCredential = false, true, "keychain" }},
	} {
		if err := initErr(t, tc.mode, tc.mutate); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: Init = %v, want INVALID_ARGUMENT", name, err)
		}
	}
}

func TestADevelopmentInstallTakesTheGhTokenAndWarnsLoudly(t *testing.T) {
	t.Parallel()
	in, exec, out := newTestInstaller(t)
	const ghTokenValue = "gho_developmentTokenFromTheGhLogin0000"
	exec.answer = func(c Cmd) ([]byte, error) {
		if c.Name == "gh" && strings.Join(c.Args, " ") == "auth token" {
			return []byte(ghTokenValue + "\n"), nil
		}
		return nil, nil
	}
	o := initOptions(in, manifest.ModeStandalone)
	o.Sim, o.Dev, o.GitHubCredential = false, true, GitHubCredentialGHCLI
	res, err := in.Init(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "DEVELOPMENT INSTALL") {
		t.Errorf("warnings = %v, want the development warning", res.Warnings)
	}
	control, _ := readEnvFile(in.Layout.RuntimeEnv(areaControl))
	if control["SISF_GITHUB_TOKEN"] != ghTokenValue {
		t.Error("control does not hold the token of the gh login")
	}
	if strings.Contains(out.String()+sprintf("%+v", res), ghTokenValue) {
		t.Error("init printed the gh token")
	}
	m, _ := in.Layout.LoadManifest()
	if !devInstall(m) || !identityOf(m).usesFake() {
		t.Error("the manifest does not record a development install signing in at the fake")
	}
	var c collector
	in.checkIdentity(&c, m)
	if v := verdictOf(c.report, "fake identity"); len(v) != 1 || v[0] != VerdictWarn {
		t.Errorf("doctor's fake identity verdict = %v, want a warning", v)
	}

	exec.answer = func(Cmd) ([]byte, error) { return nil, errors.New("not logged in") }
	other, otherExec, _ := newTestInstaller(t)
	otherExec.answer = exec.answer
	o2 := initOptions(other, manifest.ModeStandalone)
	o2.Sim, o2.Dev, o2.GitHubCredential = false, true, GitHubCredentialGHCLI
	if _, err := other.Init(t.Context(), o2); !core.HasCode(err, core.CodeUnavailable) || !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("Init without a gh login = %v, want UNAVAILABLE naming gh auth login", err)
	}
}

func TestACappedBudgetIsConfirmedWithItsCapAndRunEstimate(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	o := initOptions(in, manifest.ModeStandalone)
	o.Budget = BudgetChoice{Confirmed: true, CapMicros: 10_000_000, AlertThresholdMicros: 5_000_000}
	if _, err := in.Init(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := readBudgetConfirmation(in.Layout.BudgetConfirmation())
	if err != nil || !ok || rec.Mode != core.BudgetCapped || rec.CapMicros != 10_000_000 || rec.RunEstimateMicros != 2_000_000 {
		t.Fatalf("confirmation = %+v (%v, %v), want capped at 10 with a fifth set aside per run", rec, ok, err)
	}
	for name, choice := range map[string]BudgetChoice{
		"alert at the cap":       {Confirmed: true, CapMicros: 10, AlertThresholdMicros: 10},
		"estimate above the cap": {Confirmed: true, CapMicros: 10, RunEstimateMicros: 11},
		"alert off with a cap":   {Confirmed: true, CapMicros: 10, AlertOff: true},
		"estimate without a cap": {Confirmed: true, AlertOff: true, RunEstimateMicros: 1},
		"cap unconfirmed":        {CapMicros: 10},
	} {
		if err := choice.Validate(); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: Validate = %v, want INVALID_ARGUMENT", name, err)
		}
	}
}
