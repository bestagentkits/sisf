package wiring

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/identity"
)

// operatorTokenAccess reads what the control zone's own token may do on
// the factory's repository, for an install with no GitHub App
// installation (GitHubSettings.NoWebhooks). The token stands where the
// installation would: the factory is "installed" on the repository exactly
// when GitHub confirms the token may write to it. The returned
// Installation's ID is the repository's numeric GitHub ID, the one real
// non-zero number that names this access; nothing is made up.
func operatorTokenAccess(ctx context.Context, apiURL string, token core.SecretValue, repo github.RepoRef, client *http.Client) (identity.Installation, error) {
	if token.IsZero() {
		return identity.Installation{}, missing(EnvGitHubToken)
	}
	if apiURL == "" {
		apiURL = github.DefaultBaseURL
	}
	u := strings.TrimRight(apiURL, "/") + "/repos/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return identity.Installation{}, core.NewError(core.CodeInternal, "the repository request could not be built").WithCause(err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+string(token.Reveal()))
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return identity.Installation{}, core.NewError(core.CodeUnavailable, "GitHub could not be reached to read the token's access to the repository").WithCause(err)
	}
	defer func() { _ = resp.Body.Close() }()
	name := repo.Owner + "/" + repo.Name
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return identity.Installation{}, core.NewError(core.CodePermissionDenied,
			"the GitHub token cannot see "+name+" (answer "+strconv.Itoa(resp.StatusCode)+")")
	default:
		return identity.Installation{}, core.NewError(core.CodeUnavailable,
			"GitHub answered "+strconv.Itoa(resp.StatusCode)+" for "+name)
	}
	var reply struct {
		ID          int64           `json:"id"`
		Permissions map[string]bool `json:"permissions"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&reply); err != nil {
		return identity.Installation{}, core.NewError(core.CodeUnavailable, "GitHub's answer for "+name+" cannot be read").WithCause(err)
	}
	switch {
	case reply.ID <= 0:
		return identity.Installation{}, core.NewError(core.CodeUnavailable, "GitHub's answer for "+name+" names no repository ID")
	case !reply.Permissions["push"]:
		return identity.Installation{}, core.NewError(core.CodePermissionDenied,
			"the GitHub token may not write to "+name+": issues, comments and pull requests need write access")
	}
	return identity.Installation{ID: reply.ID, Owner: repo.Owner, Repo: repo.Name}, nil
}
