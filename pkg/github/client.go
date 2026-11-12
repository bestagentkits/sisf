package github

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// DefaultBaseURL is the public GitHub REST API.
const DefaultBaseURL = "https://api.github.com"

// TokenSource supplies the installation token for each request, so a token
// that rotates or is revoked is picked up without rebuilding the client. The
// client never stores the token.
type TokenSource func(ctx context.Context) (core.SecretValue, error)

// StaticToken is a TokenSource that always returns token.
func StaticToken(token core.SecretValue) TokenSource {
	return func(context.Context) (core.SecretValue, error) { return token, nil }
}

// ClientConfig configures a RESTClient.
type ClientConfig struct {
	// BaseURL is the API root. It must be https, except for a loopback
	// address used by tests and the simulation. Default: DefaultBaseURL.
	BaseURL string
	Token   TokenSource
	// HTTPClient carries the requests. Default: a client with a 30s timeout.
	// Redirects are never followed, so the token cannot leave the API host.
	HTTPClient *http.Client
	// Clock drives every wait, so tests need not sleep. Default: system clock.
	Clock core.Clock
	// MaxAttempts bounds tries per request, the first included (default 4).
	MaxAttempts int
	// MaxWait is the longest rate-limit wait the client sits through. A
	// longer one is returned as RATE_LIMITED for the caller to schedule
	// (default 2 minutes).
	MaxWait time.Duration
	// ServerErrorBackoff is the first wait after a 5xx; it doubles per
	// attempt (default 500ms).
	ServerErrorBackoff time.Duration
	// MaxPages bounds how many pages one listing follows (default 50).
	MaxPages int
}

// RESTClient implements Client over GitHub's REST API with an installation
// token. Rate limits are waited out within MaxWait and otherwise reported as
// RATE_LIMITED; an outage is UNAVAILABLE. Neither is ever success.
type RESTClient struct {
	base  *url.URL
	token TokenSource
	http  *http.Client
	clock core.Clock
	cfg   ClientConfig
}

var _ Client = (*RESTClient)(nil)

// NewRESTClient validates the configuration and builds the client.
func NewRESTClient(cfg ClientConfig) (*RESTClient, error) {
	if cfg.Token == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "a GitHub client needs a token source")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Host == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "the GitHub API base URL is not a valid URL")
	}
	if base.Scheme != "https" && (base.Scheme != "http" || !isLoopback(base.Hostname())) {
		return nil, core.NewError(core.CodeInvalidArgument, "the GitHub API base URL must be https, or http on a loopback address")
	}
	base.Path = strings.TrimRight(base.Path, "/")
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	hc := *cfg.HTTPClient
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	cfg.MaxAttempts = positive(cfg.MaxAttempts, 4)
	cfg.MaxPages = positive(cfg.MaxPages, 50)
	if cfg.MaxWait <= 0 {
		cfg.MaxWait = 2 * time.Minute
	}
	if cfg.ServerErrorBackoff <= 0 {
		cfg.ServerErrorBackoff = 500 * time.Millisecond
	}
	return &RESTClient{base: base, token: cfg.Token, http: &hc, clock: cfg.Clock, cfg: cfg}, nil
}

func positive(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

var (
	repoOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)
	repoNamePattern  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// validateRepo rejects a repository that could not exist, before it is put
// into a request path.
func validateRepo(repo RepoRef) error {
	if !repoOwnerPattern.MatchString(repo.Owner) || !repoNamePattern.MatchString(repo.Name) || repo.Name == "." || repo.Name == ".." {
		return core.NewError(core.CodeInvalidArgument, "repository "+repo.String()+" must be owner/name")
	}
	return nil
}

// repoPath builds "/repos/owner/name/<parts...>" with every part escaped.
func repoPath(repo RepoRef, parts ...string) string {
	segments := []string{"repos", repo.Owner, repo.Name}
	segments = append(segments, parts...)
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return "/" + strings.Join(segments, "/")
}

// maxCommentBytes is GitHub's limit for a comment body.
const maxCommentBytes = 65536
