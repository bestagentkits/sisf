package auth_test

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/identity"
)

func TestServiceConfigurationIsValidated(t *testing.T) {
	t.Parallel()
	gh, err := auth.NewGitHubProvider(auth.GitHubConfig{ClientID: "c", RedirectURL: "http://127.0.0.1:1/auth/callback/github"})
	if err != nil || gh.Issuer() != auth.GitHubIssuer || gh.Name() != "github" {
		t.Fatalf("github defaults: %v", err)
	}
	clock := core.SystemClock{}
	for name, cfg := range map[string]auth.Config{
		"no clock":         {Providers: []identity.Provider{gh}},
		"no providers":     {Clock: clock},
		"duplicate names":  {Clock: clock, Providers: []identity.Provider{gh, gh}},
		"negative ttl":     {Clock: clock, Providers: []identity.Provider{gh}, SessionTTL: -1},
		"malformed scopes": {Clock: clock, Providers: []identity.Provider{gh}, AllowedScopes: []string{"*"}},
	} {
		if _, err := auth.New(cfg); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, cfg := range map[string]auth.GitHubConfig{
		"no client":       {RedirectURL: "http://x/cb"},
		"no redirect":     {ClientID: "c"},
		"relative issuer": {ClientID: "c", RedirectURL: "http://x/cb", Issuer: "github"},
	} {
		if _, err := auth.NewGitHubProvider(cfg); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("github %s: %v", name, err)
		}
	}
	for name, cfg := range map[string]auth.OIDCConfig{
		"no clock":   {Name: "g", Issuer: "https://idp.example", ClientID: "c", RedirectURL: "http://x/cb"},
		"bad issuer": {Name: "g", Issuer: "idp", ClientID: "c", RedirectURL: "http://x/cb", Clock: clock},
	} {
		if _, err := auth.NewOIDCProvider(context.Background(), cfg); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("oidc %s: %v", name, err)
		}
	}
}

func TestOnlyAllowedScopesCanBeIssued(t *testing.T) {
	t.Parallel()
	gh, err := auth.NewGitHubProvider(auth.GitHubConfig{ClientID: "c", RedirectURL: "http://127.0.0.1:1/cb"})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := auth.New(auth.Config{Clock: core.SystemClock{}, Providers: []identity.Provider{gh}, AllowedScopes: []string{"runs:write"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.IssueServiceToken(auth.ServiceTokenRequest{ActorID: "agent:a", Kind: core.ActorAgent, Scopes: []string{"policy:write"}}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("an unknown scope was issued: %v", err)
	}
	if _, err := svc.IssueServiceToken(auth.ServiceTokenRequest{ActorID: "agent:a", Kind: core.ActorAgent, Scopes: []string{"runs:write"}}); err != nil {
		t.Fatal(err)
	}
	if svc.Active(context.Background(), "ses_unknown") || svc.RevokeToken("tok_unknown") == nil {
		t.Fatal("unknown handles must be inactive and unrevocable")
	}
}
