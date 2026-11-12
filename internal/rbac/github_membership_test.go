package rbac_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bestagentkits/sisf/internal/rbac"
	"github.com/bestagentkits/sisf/internal/testkit/fakeoidc"
	"github.com/bestagentkits/sisf/pkg/core"
)

func installationToken(context.Context, string, string) (core.SecretValue, error) {
	return core.NewSecretValue([]byte("ghs_installation")), nil
}

func TestGitHubMembershipReadsTheRepositoryPermission(t *testing.T) {
	t.Parallel()
	fake := fakeoidc.NewTestServer(t)
	fake.SetRepoPermission("acme/app", "alice", "maintain")
	m := &rbac.GitHubMembership{APIBase: fake.Issuer() + fakeoidc.GitHubAPIPrefix, Token: installationToken}
	for repo, want := range map[string]string{"app": "maintain", "other": "none"} {
		got, err := m.RepoPermission(context.Background(), "acme", repo, "alice")
		if err != nil || got != want {
			t.Errorf("acme/%s: %q %v, want %q", repo, got, err, want)
		}
	}
}

func TestGitHubMembershipFailsClosed(t *testing.T) {
	t.Parallel()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/acme/gone/collaborators/alice/permission" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(broken.Close)
	m := &rbac.GitHubMembership{APIBase: broken.URL, Token: installationToken}
	if _, err := m.RepoPermission(context.Background(), "acme", "app", "alice"); !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("a GitHub failure must be UNAVAILABLE, got %v", err)
	}
	if got, err := m.RepoPermission(context.Background(), "acme", "gone", "alice"); err != nil || got != "none" {
		t.Fatalf("a 404 means no permission: %q %v", got, err)
	}
	if _, err := (&rbac.GitHubMembership{}).RepoPermission(context.Background(), "a", "b", "c"); err == nil {
		t.Fatal("an unconfigured source answered")
	}
}

func TestMembershipFailureDeniesTheAction(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	failing, err := rbac.New(rbac.Config{
		Grants: f.grants, Sessions: f.live, Clock: f.clock, DefaultRepo: "acme/app",
		Installations: installs{"acme/app": {ID: 7, Owner: "acme", Repo: "app"}},
		Membership:    &rbac.GitHubMembership{APIBase: "http://127.0.0.1:1", Token: installationToken},
		Accounts:      logins{"user:alice": "alice"},
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := failing.Authorize(context.Background(), core.AuthzRequest{Actor: alice, Action: mergeSpec})
	if err == nil || d.Allowed() {
		t.Fatalf("an unreachable GitHub must deny with an error: %+v %v", d, err)
	}
}
