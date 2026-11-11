package wiring

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Only a development install on the operator's gh login may name no GitHub
// App installation, and it then takes no webhooks; every other real install
// is still refused without one.
func TestOnlyADevelopmentInstallOnTheOperatorsLoginGoesWithoutAnInstallation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		ext        map[string]any
		noWebhooks bool
		refused    bool
	}{
		{"dev on gh-cli", map[string]any{"profile": "default", "dev": true, "github_auth_via": "gh-cli"}, true, false},
		{"gh-cli without dev", map[string]any{"profile": "default", "github_auth_via": "gh-cli"}, false, true},
		{"dev with a token from the environment", map[string]any{"profile": "default", "dev": true}, false, true},
		{"production install", map[string]any{"profile": "default"}, false, true},
		{"dev on gh-cli naming an installation but no account", map[string]any{"profile": "default", "dev": true,
			"github_auth_via": "gh-cli", "github_installation_id": 7}, false, true},
		{"production install with an app", map[string]any{"profile": "default", "github_installation_id": 7,
			"github_app_login": "factory[bot]"}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := GitHubSettingsOf(&manifest.Manifest{Extensions: tc.ext})
			if tc.refused != core.HasCode(err, core.CodeInvalidArgument) {
				t.Fatalf("err = %v, want refused %v", err, tc.refused)
			}
			if !tc.refused && s.NoWebhooks != tc.noWebhooks {
				t.Fatalf("NoWebhooks = %v, want %v", s.NoWebhooks, tc.noWebhooks)
			}
		})
	}
}

// The token stands for the installation only when GitHub confirms it may
// write to the repository; the repository's own ID names that access.
func TestOperatorTokenAccessNeedsWriteAccessGitHubConfirms(t *testing.T) {
	t.Parallel()
	repo := github.RepoRef{Owner: "acme", Name: "app"}
	token := core.NewSecretValue([]byte("operator-token"))
	cases := []struct {
		name   string
		status int
		body   string
		want   core.ErrorCode
	}{
		{"may write", http.StatusOK, `{"id":42,"permissions":{"pull":true,"push":true}}`, ""},
		{"read only", http.StatusOK, `{"id":42,"permissions":{"pull":true,"push":false}}`, core.CodePermissionDenied},
		{"no permissions reported", http.StatusOK, `{"id":42}`, core.CodePermissionDenied},
		{"not visible", http.StatusNotFound, `{}`, core.CodePermissionDenied},
		{"token refused", http.StatusUnauthorized, `{}`, core.CodePermissionDenied},
		{"no repository ID", http.StatusOK, `{"permissions":{"push":true}}`, core.CodeUnavailable},
		{"GitHub failing", http.StatusBadGateway, `{}`, core.CodeUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/acme/app" || r.Header.Get("Authorization") != "Bearer operator-token" {
					http.Error(w, "unexpected request", http.StatusTeapot)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			inst, err := operatorTokenAccess(context.Background(), srv.URL, token, repo, srv.Client())
			if tc.want == "" {
				if err != nil || inst.ID != 42 || !inst.Covers("acme", "app") {
					t.Fatalf("got %+v, %v; want the repository covered under ID 42", inst, err)
				}
				return
			}
			if !core.HasCode(err, tc.want) || inst.Covers("acme", "app") {
				t.Fatalf("got %+v, %v; want %s and no access", inst, err, tc.want)
			}
		})
	}
}
