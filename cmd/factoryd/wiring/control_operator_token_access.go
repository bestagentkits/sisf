package wiring

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/internal/actions/runactions"
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

// OperatorAccessInterval is how often an install acting with the
// operator's token checks again that the token may still write to the
// repository.
const OperatorAccessInterval = 5 * time.Minute

// Verdicts of the operator token's access, as the factory's status shows them.
const (
	tokenAccessConfirmed = "confirmed"
	tokenAccessRevoked   = "revoked"
)

// operatorAccess keeps the operator token's access to the repository under
// watch. The control zone checks it again every OperatorAccessInterval and
// fails closed: once GitHub says the token may no longer write to the
// repository, or now names another repository, the control zone's passes do
// nothing on GitHub until a later check confirms the access again, and the
// factory's status says why. The surfaces stay up so the Owner can read
// that; GitHub itself refuses what the token may no longer do. A check that
// cannot reach GitHub changes nothing: an outage is not a revocation.
type operatorAccess struct {
	apiURL string
	token  core.SecretValue
	want   identity.Installation
	clock  core.Clock
	client *http.Client

	mu sync.Mutex
	// next is when the next check is due.
	next time.Time
	// revoked is why access was lost; "" while it is confirmed.
	revoked string
	// unreachable is why the last check could not decide; "" when it did.
	unreachable string
	checkedAt   time.Time
}

// newOperatorAccess watches the access confirmed at start as want.
func newOperatorAccess(apiURL string, token core.SecretValue, want identity.Installation, clock core.Clock) *operatorAccess {
	return &operatorAccess{apiURL: apiURL, token: token, want: want, clock: clock, client: &http.Client{Timeout: identityTimeout},
		checkedAt: clock.Now(), next: clock.Now().Add(OperatorAccessInterval)}
}

// check asks GitHub again when a check is due, and returns PERMISSION_DENIED
// while the access is revoked.
func (a *operatorAccess) check(ctx context.Context) error {
	a.mu.Lock()
	due := !a.clock.Now().Before(a.next)
	a.mu.Unlock()
	if due {
		got, err := operatorTokenAccess(ctx, a.apiURL, a.token, github.RepoRef{Owner: a.want.Owner, Name: a.want.Repo}, a.client)
		a.mu.Lock()
		now := a.clock.Now()
		a.next = now.Add(OperatorAccessInterval)
		switch {
		case core.HasCode(err, core.CodePermissionDenied):
			a.revoked, a.unreachable, a.checkedAt = reasonOf(err), "", now
		case err != nil:
			a.unreachable = reasonOf(err)
		case got.ID != a.want.ID:
			a.revoked, a.unreachable, a.checkedAt = "GitHub now names another repository at "+a.want.Owner+"/"+a.want.Repo, "", now
		default:
			a.revoked, a.unreachable, a.checkedAt = "", "", now
		}
		a.mu.Unlock()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.revoked != "" {
		return core.NewError(core.CodePermissionDenied,
			"the operator token lost write access to "+a.want.Owner+"/"+a.want.Repo+"; nothing is done on GitHub until it is restored: "+a.revoked)
	}
	return nil
}

// status reports the verdict for the factory's status.
func (a *operatorAccess) status(out *runactions.GitHubSync) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out.TokenAccess, out.TokenCheckedAt = tokenAccessConfirmed, a.checkedAt
	switch {
	case a.revoked != "":
		out.TokenAccess, out.TokenAccessDetail = tokenAccessRevoked, a.revoked
	case a.unreachable != "":
		out.TokenAccessDetail = "the last check could not reach GitHub: " + a.unreachable
	}
}
