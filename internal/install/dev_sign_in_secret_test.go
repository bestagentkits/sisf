package install

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// unitsOf builds the Standalone units of the install of in.
func unitsOf(t *testing.T, in *Installer) []unit {
	t.Helper()
	m, err := in.Layout.LoadManifest()
	must(t, err)
	units, closers, err := in.standaloneUnits(m)
	must(t, err)
	for _, c := range closers {
		t.Cleanup(func() { _ = c.Close() })
	}
	return units
}

func unitNamed(t *testing.T, units []unit, name string) unit {
	t.Helper()
	for _, u := range units {
		if u.name == name {
			return u
		}
	}
	t.Fatalf("no unit %s", name)
	return unit{}
}

// A development install runs real agents as local processes next to the
// fake identity provider, which signs in whoever reaches it. Its sign-in is
// locked behind a secret only the operator reads: a control secret file,
// never on a command line, in a unit's environment or in the output.
func TestADevelopmentInstallLocksSignInAtTheFakeBehindAnOperatorOnlySecret(t *testing.T) {
	t.Parallel()
	in, exec, out := newTestInstaller(t)
	exec.answer = func(c Cmd) ([]byte, error) {
		if c.Name == "gh" && strings.Join(c.Args, " ") == "auth token" {
			return []byte("gho_developmentTokenFromTheGhLogin0000\n"), nil
		}
		return nil, nil
	}
	o := initOptions(in, manifest.ModeStandalone)
	o.Sim, o.Dev, o.GitHubCredential = false, true, GitHubCredentialGHCLI
	o.Agents = AgentOptions{Coder: "claude", AgentHome: t.TempDir()}
	if _, err := in.Init(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	units := unitsOf(t, in)
	path := in.Layout.DevSignInSecret()
	if !slices.Contains(unitNamed(t, units, "sim-oidc").cmd.Args, "--sign-in-secret-file="+path) {
		t.Fatalf("the fake identity provider is not locked: %v", unitNamed(t, units, "sim-oidc").cmd.Args)
	}
	info, err := os.Stat(path)
	must(t, err)
	if info.Mode().Perm() != secretFileMode {
		t.Errorf("the sign-in secret is %v, want owner-only", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	must(t, err)
	secret := strings.TrimSpace(string(raw))
	if len(secret) < 40 {
		t.Fatalf("the sign-in secret is %d characters, want a 256-bit token", len(secret))
	}
	for _, u := range units {
		if strings.Contains(strings.Join(u.cmd.Args, " ")+strings.Join(u.cmd.Env, " "), secret) {
			t.Errorf("the unit %s is given the sign-in secret", u.name)
		}
	}
	if strings.Contains(out.String(), secret) || !strings.Contains(out.String(), path) {
		t.Error("up must name the secret's file and never print the secret")
	}
	// Every start makes a new secret.
	unitsOf(t, in)
	again, err := os.ReadFile(path)
	must(t, err)
	if strings.TrimSpace(string(again)) == secret {
		t.Error("a new start kept the old sign-in secret")
	}
}

func TestTheSimulationsFakeIdentityProviderStaysOpen(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeStandalone)
	args := unitNamed(t, unitsOf(t, in), "sim-oidc").cmd.Args
	if slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "--sign-in-secret-file") }) {
		t.Fatalf("the simulation's fake is locked: %v", args)
	}
	if _, err := os.Stat(in.Layout.DevSignInSecret()); !os.IsNotExist(err) {
		t.Errorf("the simulation wrote a sign-in secret: %v", err)
	}
}
