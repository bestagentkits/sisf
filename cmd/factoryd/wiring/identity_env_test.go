package wiring_test

import (
	"testing"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/identity"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

func profileManifest(profile string) *manifest.Manifest {
	return &manifest.Manifest{Extensions: map[string]any{"profile": profile}}
}

func envOf(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// The simulation profile signs people in at the fake identity provider of
// `sisf sim oidc`, which also stands for GitHub, with "owner" as the Owner.
func TestTheSimulationProfileDefaultsToTheFakeIdentityProvider(t *testing.T) {
	t.Parallel()
	cfg, err := wiring.IdentityFromEnv(envOf(nil), profileManifest("sim"), "127.0.0.1:7463")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExternalURL != "http://127.0.0.1:7463" || cfg.OIDC == nil || cfg.OIDC.Issuer != wiring.SimOIDCIssuer ||
		cfg.OIDC.ClientID != wiring.SimOIDCClientID {
		t.Fatalf("sim sign-in: %+v %+v", cfg, cfg.OIDC)
	}
	if cfg.GitHub == nil || cfg.GitHub.WebURL != wiring.SimOIDCIssuer || cfg.GitHub.APIURL != wiring.SimOIDCIssuer+"/api" {
		t.Fatalf("sim GitHub sign-in: %+v", cfg.GitHub)
	}
	if want := (identity.Identity{Issuer: wiring.SimOIDCIssuer, Subject: wiring.SimOwnerSubject}); len(cfg.Owners) != 1 || cfg.Owners[0] != want {
		t.Fatalf("sim Owner: %+v", cfg.Owners)
	}
}

// A real install names its providers and its Owners; nothing is defaulted,
// and a control server off loopback needs its external URL.
func TestARealInstallMustNameItsSignInAndItsOwners(t *testing.T) {
	t.Parallel()
	standalone := profileManifest("standalone")
	complete := map[string]string{
		wiring.EnvOIDCIssuer: "https://accounts.example.test", wiring.EnvOIDCClientID: "client",
		wiring.EnvOIDCClientSecret: "client-secret", wiring.EnvOwnerIdentities: "https://accounts.example.test#1234",
		wiring.EnvExternalURL: "https://factory.example.test/",
	}
	cfg, err := wiring.IdentityFromEnv(envOf(complete), standalone, "0.0.0.0:7463")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExternalURL != "https://factory.example.test" || cfg.GitHub != nil || cfg.OIDC.ClientSecret.IsZero() ||
		cfg.Owners[0].Subject != "1234" {
		t.Fatalf("the configured sign-in: %+v", cfg)
	}
	for name, change := range map[string]map[string]string{
		"no provider":                  {wiring.EnvOIDCIssuer: ""},
		"no client id":                 {wiring.EnvOIDCClientID: ""},
		"no Owner":                     {wiring.EnvOwnerIdentities: ""},
		"an Owner without issuer":      {wiring.EnvOwnerIdentities: "1234"},
		"an Owner without subject":     {wiring.EnvOwnerIdentities: "https://accounts.example.test#"},
		"no external URL off loopback": {wiring.EnvExternalURL: ""},
		"an external URL with a path":  {wiring.EnvExternalURL: "https://factory.example.test/sisf"},
	} {
		vars := map[string]string{}
		for k, v := range complete {
			vars[k] = v
		}
		for k, v := range change {
			vars[k] = v
		}
		if _, err := wiring.IdentityFromEnv(envOf(vars), standalone, "0.0.0.0:7463"); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s was accepted: %v", name, err)
		}
	}
}
