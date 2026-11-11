package install

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// The doctor reads the GitHub settings as control and the supervisor do:
// a real install that names no GitHub App installation fails the check
// instead of passing while its units exit at every start, and a
// development install on the operator's gh login is told it polls, in
// Standalone mode only.
func TestTheDoctorFailsGitHubSettingsTheUnitsWouldRefuse(t *testing.T) {
	t.Parallel()
	devOnLogin := map[string]any{"profile": "default", "dev": true, extGitHubCredential: GitHubCredentialGHCLI, extReconcileInterval: "15s"}
	cases := map[string]struct {
		mode manifest.Mode
		ext  map[string]any
		want Verdict
	}{
		"no installation":     {manifest.ModeStandalone, map[string]any{"profile": "default"}, VerdictFail},
		"dev on the gh login": {manifest.ModeStandalone, devOnLogin, VerdictWarn},
		"named app":           {manifest.ModeDocker, map[string]any{"profile": "default", "github_installation_id": 7, "github_app_login": "f[bot]"}, VerdictOK},
		// An account named by its ID alone cannot tell the factory's own
		// pull request, so no candidate is ever merged.
		"app named by its ID only": {manifest.ModeDocker, map[string]any{"profile": "default", "github_installation_id": 7, "github_app_id": 42}, VerdictWarn},
		"simulation":               {manifest.ModeDocker, map[string]any{"profile": "sim"}, VerdictOK},
		"gh login without --dev":   {manifest.ModeStandalone, map[string]any{"profile": "default", extGitHubCredential: GitHubCredentialGHCLI}, VerdictFail},
		// A manifest changed by hand to Docker mode keeps no exception.
		"dev on the gh login in Docker mode": {manifest.ModeDocker, devOnLogin, VerdictFail},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var c collector
			checkGitHubEvents(&c, &manifest.Manifest{Mode: tc.mode, Extensions: tc.ext})
			if len(c.report.Checks) != 1 || c.report.Checks[0].Verdict != tc.want {
				t.Fatalf("checks = %+v, want one %s", c.report.Checks, tc.want)
			}
			if name == "app named by its ID only" {
				if !strings.Contains(c.report.Checks[0].Detail, "github_app_login") {
					t.Fatalf("the warning does not name the missing login: %s", c.report.Checks[0].Detail)
				}
				return
			}
			if tc.want == VerdictWarn && !strings.Contains(c.report.Checks[0].Detail, "every 15s") {
				t.Fatalf("the warning does not name the install's interval: %s", c.report.Checks[0].Detail)
			}
		})
	}
}
