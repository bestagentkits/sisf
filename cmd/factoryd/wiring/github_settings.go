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
}

// GitHubSettingsOf reads the GitHub settings of an install. The simulation
// profile defaults them to the fake GitHub's; a real install must state
// them, because accepting a webhook of any installation or mistaking a
// person for the factory's own account would let anyone drive it.
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
