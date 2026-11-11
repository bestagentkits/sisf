package wiring

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/bestagentkits/sisf/internal/testkit/fakegithub"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Manifest extensions that describe the GitHub App the factory acts as.
// They are not secret: the installation ID and the app's account are what
// the webhook route accepts and what the loop guard recognizes.
const (
	extProfile        = "profile"
	extGitHubAPIURL   = "github_api_url"
	extInstallationID = "github_installation_id"
	extAppLogin       = "github_app_login"
	extAppID          = "github_app_id"
	profileSim        = "sim"
	// extDev and extGitHubAuthVia are written by the installer: a
	// development install, and where its GitHub token comes from.
	extDev           = "dev"
	extGitHubAuthVia = "github_auth_via"
	githubAuthGHCLI  = "gh-cli"
)

// SimulationProfile reports whether m declares the trusted simulation
// profile: a fake GitHub and a scripted agent, where no code an agent wrote
// is ever run.
func SimulationProfile(m *manifest.Manifest) bool { return m.Extensions[extProfile] == profileSim }

// GitHubSettings are the GitHub facts the control zone needs.
type GitHubSettings struct {
	APIURL         string
	InstallationID int64
	Self           github.Identity
	// NoWebhooks marks an install with no GitHub App installation: a
	// development install acting with the operator's own gh login, on
	// loopback, where GitHub cannot deliver a webhook. It accepts no
	// delivery at all and learns of issues only by reconciliation; its repository
	// access is what the token itself is granted.
	NoWebhooks bool
}

// operatorTokenOnly reports whether m is a development install that acts
// with the operator's gh login and names no GitHub App installation.
func operatorTokenOnly(m *manifest.Manifest) bool {
	dev, _ := m.Extensions[extDev].(bool)
	_, named := m.Extensions[extInstallationID]
	return dev && m.Extensions[extGitHubAuthVia] == githubAuthGHCLI && !named
}

// GitHubSettingsOf reads the GitHub settings of an install. The simulation
// profile defaults them to the fake GitHub's; a real install must state
// them, because accepting a webhook of any installation or mistaking a
// person for the factory's own account would let anyone drive it. The one
// exception is a development install on the operator's gh login, in
// Standalone mode, which has no installation to state: it takes no webhook
// at all.
func GitHubSettingsOf(m *manifest.Manifest) (GitHubSettings, error) {
	s := GitHubSettings{APIURL: github.DefaultBaseURL}
	if url, ok := m.Extensions[extGitHubAPIURL].(string); ok && url != "" {
		s.APIURL = url
	}
	sim := SimulationProfile(m)
	if sim {
		bot := fakegithub.New().Bot()
		s.InstallationID, s.Self = fakegithub.DefaultInstallationID, github.Identity{ID: bot.ID, Login: bot.Login}
	}
	if raw, ok := m.Extensions[extInstallationID]; ok {
		id, err := integer(raw)
		if err != nil || id <= 0 {
			return s, core.NewError(core.CodeInvalidArgument, "extensions."+extInstallationID+" must be a positive number")
		}
		s.InstallationID = id
	}
	if login, ok := m.Extensions[extAppLogin].(string); ok && login != "" {
		s.Self = github.Identity{Login: login}
	}
	if raw, ok := m.Extensions[extAppID]; ok {
		id, err := integer(raw)
		if err != nil || id <= 0 {
			return s, core.NewError(core.CodeInvalidArgument, "extensions."+extAppID+" must be a positive number")
		}
		s.Self.ID = id
	}
	if !sim && operatorTokenOnly(m) {
		// The installer makes such an install Standalone, where every
		// listen address is loopback; a manifest that says otherwise was
		// changed by hand and gets no exception.
		if m.Mode != manifest.ModeStandalone {
			return s, core.NewError(core.CodeInvalidArgument,
				"a development install on the operator's gh login runs in Standalone mode only; this one is in mode "+fmt.Sprintf("%q", m.Mode)+
					" and must name a GitHub App installation in extensions."+extInstallationID)
		}
		s.NoWebhooks = true
		return s, nil
	}
	switch {
	case s.InstallationID == 0:
		return s, core.NewError(core.CodeInvalidArgument, "the manifest must name the GitHub installation in extensions."+extInstallationID)
	case s.Self.IsZero():
		return s, core.NewError(core.CodeInvalidArgument, "the manifest must name the factory's GitHub account in extensions."+extAppLogin)
	}
	return s, nil
}

// integer reads a number YAML decoded as an int, a float or a string.
func integer(raw any) (int64, error) {
	switch v := raw.(type) {
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case float64:
		if v != float64(int64(v)) {
			return 0, fmt.Errorf("not a whole number")
		}
		return int64(v), nil
	case string:
		return strconv.ParseInt(v, 10, 64)
	}
	return 0, fmt.Errorf("not a number")
}

// NewGitHubClient builds the REST client of the control zone. The client
// itself refuses plain http to anything but a loopback address.
func NewGitHubClient(s GitHubSettings, token core.SecretValue, clock core.Clock) (*github.RESTClient, error) {
	if token.IsZero() {
		return nil, missing(EnvGitHubToken)
	}
	return github.NewRESTClient(github.ClientConfig{
		BaseURL: s.APIURL, Token: github.StaticToken(token), Clock: clock,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	})
}
