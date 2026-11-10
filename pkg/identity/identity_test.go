package identity_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/core"
	"github.com/bestagentkits/sisf/pkg/identity"
)

func TestIdentityIsKeyedByIssuerAndSubjectNeverByEmail(t *testing.T) {
	google := identity.Identity{Issuer: "https://accounts.google.com", Subject: "1234567890", Email: "owner@example.com", EmailVerified: true}
	github := identity.Identity{Issuer: "https://github.com", Subject: "1234567890", Email: "owner@example.com", EmailVerified: true}
	if google.Key() != "https://accounts.google.com#1234567890" {
		t.Fatalf("Key() = %q", google.Key())
	}
	if google.Same(github) || google.Key() == github.Key() {
		t.Fatal("the same subject and email at another issuer is a different identity")
	}
	renamed := google
	renamed.Email = "new-address@example.com"
	if !google.Same(renamed) {
		t.Fatal("changing the email does not change who the identity is")
	}
	impostor := identity.Identity{Issuer: "https://accounts.google.com", Subject: "999", Email: google.Email, EmailVerified: true}
	if google.Same(impostor) {
		t.Fatal("a matching email alone must never match an identity")
	}
	if (identity.Identity{}).Same(identity.Identity{}) {
		t.Fatal("two empty identities are nobody, not the same person")
	}
}

func TestIdentityValidation(t *testing.T) {
	good := identity.Identity{Issuer: "http://localhost:7468", Subject: "user-1"}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}
	for name, id := range map[string]identity.Identity{
		"no issuer":            {Subject: "user-1"},
		"no subject":           {Issuer: "https://accounts.google.com"},
		"email only":           {Email: "owner@example.com", EmailVerified: true},
		"issuer with fragment": {Issuer: "https://idp.example.com#x", Subject: "user-1"},
		"issuer with space":    {Issuer: "https://idp.example.com /", Subject: "user-1"},
		"issuer not a url":     {Issuer: "google", Subject: "user-1"},
	} {
		if core.CodeOf(id.Validate()) != core.CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, id.Validate())
		}
	}
}

func TestInstallationTokenDoesNotPrintItsSecret(t *testing.T) {
	token := identity.InstallationToken{
		Token:     core.NewSecretValue([]byte("ghs_installation-token")),
		ExpiresAt: time.Date(2026, 11, 10, 7, 0, 0, 0, time.UTC),
	}
	if strings.Contains(strings.Join([]string{token.Token.String(), string(mustText(t, token.Token))}, " "), "ghs_installation-token") {
		t.Fatal("the installation token leaked")
	}
	if token.Expired(token.ExpiresAt.Add(-time.Second)) || !token.Expired(token.ExpiresAt) {
		t.Fatal("a token is usable only before its expiry")
	}
}

func mustText(t *testing.T, s core.SecretValue) []byte {
	t.Helper()
	b, err := s.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// loginStub and repoStub are deliberately separate types: signing in and
// holding repository permissions are different contracts.
type loginStub struct{}

func (loginStub) Name() string   { return "google" }
func (loginStub) Issuer() string { return "https://accounts.google.com" }
func (loginStub) AuthorizeURL(req identity.AuthRequest) (string, error) {
	return "https://accounts.google.com/o/oauth2/v2/auth?state=" + req.State, nil
}
func (loginStub) Exchange(_ context.Context, ex identity.Exchange) (identity.Identity, error) {
	if ex.Nonce == "" {
		return identity.Identity{}, core.NewError(core.CodeUnauthenticated, "nonce mismatch")
	}
	return identity.Identity{Issuer: "https://accounts.google.com", Subject: "1"}, nil
}

type repoStub struct{}

func (repoStub) Installation(_ context.Context, owner, repo string) (identity.Installation, error) {
	return identity.Installation{ID: 1, Owner: owner, Repo: repo}, nil
}
func (repoStub) Token(context.Context, identity.TokenRequest) (identity.InstallationToken, error) {
	return identity.InstallationToken{}, nil
}
func (repoStub) Revoke(context.Context, identity.InstallationToken) error { return nil }

func TestLoginAndRepositoryPermissionAreSeparateContracts(t *testing.T) {
	var login identity.Provider = loginStub{}
	var repos identity.InstallationProvider = repoStub{}
	if _, ok := login.(identity.InstallationProvider); ok {
		t.Fatal("a login provider must not double as a repository permission provider")
	}
	if _, ok := repos.(identity.Provider); ok {
		t.Fatal("a repository permission provider must not double as a login provider")
	}
	if _, err := login.Exchange(context.Background(), identity.Exchange{Code: "c"}); !core.HasCode(err, core.CodeUnauthenticated) {
		t.Fatalf("a failed validation must be UNAUTHENTICATED, got %v", err)
	}
	inst, err := repos.Installation(context.Background(), "acme", "app")
	if err != nil || inst.Covers("acme", "other") || !inst.Covers("acme", "app") {
		t.Fatalf("Installation = %+v, %v", inst, err)
	}
	suspended := inst
	suspended.Suspended = true
	if suspended.Covers("acme", "app") {
		t.Fatal("a suspended installation grants nothing")
	}
}
