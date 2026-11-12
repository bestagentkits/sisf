package rbac

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/bestagentkits/sisf/pkg/core"
)

// MembershipSource reports a GitHub user's current permission on a
// repository: "admin", "maintain", "write", "triage", "read" or "none".
type MembershipSource interface {
	RepoPermission(ctx context.Context, owner, repo, login string) (string, error)
}

// RoleForPermission maps a GitHub repository permission to a workspace role.
// Admin and maintain make a Maintainer, write and triage a Reporter, read a
// Viewer. No GitHub permission makes an Owner: the Owner is granted locally.
func RoleForPermission(permission string) (core.Role, bool) {
	switch permission {
	case "admin", "maintain":
		return core.RoleMaintainer, true
	case "write", "triage":
		return core.RoleReporter, true
	case "read":
		return core.RoleViewer, true
	}
	return "", false
}

// GitHubMembership reads permissions from the GitHub REST API with the
// factory's installation token for the repository. A person's login token is
// never used for this, so signing in grants nothing by itself.
type GitHubMembership struct {
	// APIBase is the REST API root, for example https://api.github.com.
	APIBase    string
	HTTPClient *http.Client
	// Token returns an installation token for owner/repo.
	Token func(ctx context.Context, owner, repo string) (core.SecretValue, error)
}

// RepoPermission asks GitHub for login's permission on owner/repo. A user
// GitHub does not know on the repository has "none".
func (m *GitHubMembership) RepoPermission(ctx context.Context, owner, repo, login string) (string, error) {
	if m.APIBase == "" || m.Token == nil {
		return "", core.NewError(core.CodeInvalidArgument, "GitHub membership needs an API base and an installation token source")
	}
	tok, err := m.Token(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	u := m.APIBase + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) +
		"/collaborators/" + url.PathEscape(login) + "/permission"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", core.NewError(core.CodeInternal, "the permission request could not be built").WithCause(err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+string(tok.Reveal()))
	client := m.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", core.NewError(core.CodeUnavailable, "GitHub could not be reached for a permission check").WithCause(err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return "none", nil
	default:
		return "", core.NewError(core.CodeUnavailable, "GitHub answered a permission check with status "+strconv.Itoa(resp.StatusCode))
	}
	var body struct {
		Permission string `json:"permission"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", core.NewError(core.CodeUnavailable, "GitHub returned a malformed permission").WithCause(err)
	}
	return body.Permission, nil
}
