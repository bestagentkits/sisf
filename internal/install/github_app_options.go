package install

import (
	"regexp"
	"strings"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Extension keys of the GitHub App the factory acts as. Control and the
// supervisor read them at every start and refuse to start without them.
const (
	extGitHubInstallationID = "github_installation_id"
	extGitHubAppLogin       = "github_app_login"
	extGitHubAppID          = "github_app_id"
)

// githubLoginPattern is a GitHub account login, with the "[bot]" suffix
// of a GitHub App's account.
var githubLoginPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})(?:\[bot\])?$`)

// GitHubApp names the GitHub App installation a real install acts for: the
// installation whose webhooks control accepts, and the account the factory
// writes as, which is how the supervisor tells its own pull requests from
// anyone else's.
type GitHubApp struct {
	InstallationID int64
	Login          string
	// AppID is optional: the numeric id of the factory's account.
	AppID int64
}

// needsGitHubApp reports whether the install must name its GitHub App: every
// install but the simulation, which has the fake GitHub's, and a development
// install on the operator's gh login, which takes no webhooks.
func (o Options) needsGitHubApp() bool {
	return !o.Sim && (!o.Dev || o.GitHubCredential != GitHubCredentialGHCLI)
}

// checkGitHubApp validates the GitHub App settings against the install.
func (o Options) checkGitHubApp() error {
	a := o.GitHubApp
	switch {
	case a.InstallationID < 0 || a.AppID < 0:
		return invalidf("--github-installation-id and --github-app-id must be positive numbers")
	case a.Login != "" && !githubLoginPattern.MatchString(a.Login):
		return invalidf("--github-app-login %q is not a GitHub login such as my-factory[bot]", a.Login)
	case o.Sim && a != GitHubApp{}:
		return invalidf("the simulation acts as the fake GitHub's own app; do not pass --github-installation-id, --github-app-login or --github-app-id")
	case !o.needsGitHubApp():
		return nil
	}
	var missing []string
	if a.InstallationID == 0 {
		missing = append(missing, "--github-installation-id N (the installation of your GitHub App on "+o.Repo.Owner+")")
	}
	if strings.TrimSpace(a.Login) == "" {
		missing = append(missing, "--github-app-login LOGIN (the account the factory writes as, such as my-factory[bot])")
	}
	if len(missing) > 0 {
		return invalidf("a real install acts for a GitHub App and cannot start without it; pass %s", strings.Join(missing, " and "))
	}
	return nil
}

// githubAppExtensions records the GitHub App in the manifest.
func (o Options) githubAppExtensions(ext map[string]any) {
	if o.GitHubApp.InstallationID > 0 {
		ext[extGitHubInstallationID] = o.GitHubApp.InstallationID
	}
	if o.GitHubApp.Login != "" {
		ext[extGitHubAppLogin] = o.GitHubApp.Login
	}
	if o.GitHubApp.AppID > 0 {
		ext[extGitHubAppID] = o.GitHubApp.AppID
	}
}

// checkStartable reads the manifest init would write the way control and
// the supervisor read it at start, so init refuses what they would refuse
// instead of writing an install that cannot start.
func checkStartable(m *manifest.Manifest) error {
	if _, err := wiring.GitHubSettingsOf(m); err != nil {
		return invalidf("the factory could not start with these settings: %s", errorText(err))
	}
	return nil
}
