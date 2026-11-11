package wiring

import (
	"github.com/bestagentkits/sisf/internal/manifestsettings"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// The GitHub settings of an install are read by package manifestsettings,
// which the supervisor links instead of this package; the names below
// forward to it so the control unit's callers are unchanged.

// extAppLogin names the factory's own GitHub account in the manifest.
const extAppLogin = manifestsettings.ExtAppLogin

// SimulationProfile reports whether m declares the trusted simulation
// profile; see manifestsettings.SimulationProfile.
func SimulationProfile(m *manifest.Manifest) bool { return manifestsettings.SimulationProfile(m) }

// GitHubSettings are the GitHub facts the control zone needs.
type GitHubSettings = manifestsettings.GitHubSettings

// GitHubSettingsOf reads the GitHub settings of an install; see
// manifestsettings.GitHubSettingsOf.
func GitHubSettingsOf(m *manifest.Manifest) (GitHubSettings, error) {
	return manifestsettings.GitHubSettingsOf(m)
}

// NewGitHubClient builds the REST client of the control zone; see
// manifestsettings.NewGitHubClient.
func NewGitHubClient(s GitHubSettings, token core.SecretValue, clock core.Clock) (*github.RESTClient, error) {
	return manifestsettings.NewGitHubClient(s, token, clock)
}
