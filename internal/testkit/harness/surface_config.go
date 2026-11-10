package harness

import (
	"context"
	"sync"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/internal/testkit/fakeoidc"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/identity"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// The people of the harness. The Owner signs in with OpenID Connect and is
// the Owner by a local grant; the others sign in with GitHub, and their
// roles come from their permission on the factory's repository.
const (
	OwnerSubject      = "owner"
	MaintainerSubject = "maintainer"
	ViewerSubject     = "viewer"
	StrangerSubject   = "stranger"
	oidcClientSecret  = "harness-oidc-client-secret"
	githubToken       = "harness-github-token"
)

// surfaceConfig starts the fake identity provider and returns the sign-in
// and surface configuration of a control zone served at base.
func (f *Factory) surfaceConfig(base string) *wiring.SurfaceConfig {
	f.Identity = fakeoidc.NewTestServer(f.t, fakeoidc.WithClock(f.Clock))
	f.Identity.AddClient(fakeoidc.Client{ID: wiring.SimOIDCClientID, Secret: oidcClientSecret,
		RedirectURIs: []string{base + "/auth/callback/" + wiring.ProviderOIDC, base + "/auth/callback/" + wiring.ProviderGitHub}})
	for i, sub := range []string{OwnerSubject, MaintainerSubject, ViewerSubject, StrangerSubject} {
		f.Identity.AddUser(fakeoidc.User{Subject: sub, Email: sub + "@example.test", EmailVerified: true, Name: sub,
			Login: sub, GitHubID: int64(1001 + i)})
	}
	f.Identity.SetRepoPermission(f.Repo, MaintainerSubject, "maintain")
	f.Identity.SetRepoPermission(f.Repo, ViewerSubject, "read")
	issuer := f.Identity.Issuer()
	return &wiring.SurfaceConfig{
		Identity: wiring.IdentityConfig{ExternalURL: base,
			OIDC: &wiring.OIDCLogin{Issuer: issuer, ClientID: wiring.SimOIDCClientID, ClientSecret: secretOf(oidcClientSecret)},
			GitHub: &wiring.GitHubLogin{WebURL: issuer, APIURL: issuer + "/api", ClientID: wiring.SimOIDCClientID,
				ClientSecret: secretOf(oidcClientSecret)},
			Owners: []identity.Identity{{Issuer: issuer, Subject: OwnerSubject}}},
		GitHubToken: secretOf(githubToken), Supervisor: f.Supervisor, Telemetry: f.Telemetry, Version: "harness",
	}
}

func secretOf(s string) core.SecretValue { return core.NewSecretValue([]byte(s)) }

// FakeSupervisor stands for the release supervisor's admin socket. It
// records what control asked of it and answers from Units, the last known
// good release of each unit.
type FakeSupervisor struct {
	mu        sync.Mutex
	Units     map[manifest.UnitName]core.Digest
	Stops     []supervisor.EmergencyStopRequest
	Rollbacks []supervisor.RollbackRequest
}

// Status reports the recorded last known good releases.
func (s *FakeSupervisor) Status(context.Context) (supervisor.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	units := map[manifest.UnitName]core.Digest{}
	for k, v := range s.Units {
		units[k] = v
	}
	return supervisor.Status{LKG: supervisor.LKGStatus{Recorded: len(units) > 0, Units: units}}, nil
}

// EmergencyStop records the stop. Every stop answers the same report, so
// the same request gives the same answer whichever surface sent it.
func (s *FakeSupervisor) EmergencyStop(_ context.Context, req supervisor.EmergencyStopRequest) (supervisor.EmergencyStopReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Stops = append(s.Stops, req)
	return supervisor.EmergencyStopReport{StopID: "harness-stop", PublishingFrozen: true}, nil
}

// RequestRollback records the rollback.
func (s *FakeSupervisor) RequestRollback(_ context.Context, req supervisor.RollbackRequest) (supervisor.RollbackReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Rollbacks = append(s.Rollbacks, req)
	return supervisor.RollbackReport{Unit: req.Unit, RestoredTo: core.ReleaseDigest(s.Units[manifest.UnitName(req.Unit)]), Healthy: true}, nil
}

// SetLKG records digest as the last known good release of unit.
func (s *FakeSupervisor) SetLKG(unit string, digest core.Digest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Units == nil {
		s.Units = map[manifest.UnitName]core.Digest{}
	}
	s.Units[manifest.UnitName(unit)] = digest
}

// Recorded returns copies of the stops and rollbacks asked for so far.
func (s *FakeSupervisor) Recorded() ([]supervisor.EmergencyStopRequest, []supervisor.RollbackRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]supervisor.EmergencyStopRequest(nil), s.Stops...), append([]supervisor.RollbackRequest(nil), s.Rollbacks...)
}
