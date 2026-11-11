package install

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// The doctor reads the GitHub settings as control and the supervisor do:
// a real install that names no GitHub App installation fails the check
// instead of passing while its units exit at every start, and a
// development install on the operator's gh login is told it polls.
func TestTheDoctorFailsGitHubSettingsTheUnitsWouldRefuse(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		ext  map[string]any
		want Verdict
	}{
		"no installation":        {map[string]any{"profile": "default"}, VerdictFail},
		"dev on the gh login":    {map[string]any{"profile": "default", "dev": true, extGitHubCredential: GitHubCredentialGHCLI, extReconcileInterval: "15s"}, VerdictWarn},
		"named app":              {map[string]any{"profile": "default", "github_installation_id": 7, "github_app_login": "f[bot]"}, VerdictOK},
		"simulation":             {map[string]any{"profile": "sim"}, VerdictOK},
		"gh login without --dev": {map[string]any{"profile": "default", extGitHubCredential: GitHubCredentialGHCLI}, VerdictFail},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var c collector
			checkGitHubEvents(&c, &manifest.Manifest{Extensions: tc.ext})
			if len(c.report.Checks) != 1 || c.report.Checks[0].Verdict != tc.want {
				t.Fatalf("checks = %+v, want one %s", c.report.Checks, tc.want)
			}
			if tc.want == VerdictWarn && !strings.Contains(c.report.Checks[0].Detail, "every 15s") {
				t.Fatalf("the warning does not name the install's interval: %s", c.report.Checks[0].Detail)
			}
		})
	}
}
