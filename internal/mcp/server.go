// Package mcp is the remote MCP surface of the factory: a streamable-HTTP
// endpoint that offers every registered action as a tool.
//
// Each tool is one core.ActionSpec. Its input schema is the same envelope
// the REST surface takes: the action's input plus dry_run, idempotency_key
// and, for a sensitive action, approval_id. Every call goes through the one
// action service with surface MCP and the actor taken from the verified
// bearer token, so a tool's description, which a model reads, never grants
// anything: the service decides from the person's current roles and the
// token's scopes alone.
//
// A request without a valid bearer token is answered 401 with a
// WWW-Authenticate header naming the Protected Resource Metadata, so a
// client can discover the authorization server. A browser session cookie is
// never read here, so an ambient session cannot drive the endpoint.
//
// The endpoint is stateless: every HTTP request is authenticated on its own
// and nothing outlives it.
package mcp

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Path is where the endpoint is mounted.
const Path = "/mcp"

// maxRequestBytes bounds one JSON-RPC request body.
const maxRequestBytes = 1 << 20

// Actions is the action service; *actions.Service is one.
type Actions interface {
	Specs() []core.ActionSpec
	Invoke(ctx context.Context, name string, req core.ActionRequest) (core.ActionResult, error)
}

// AccessTokens verifies OAuth access tokens; *oauthas.Server is one.
type AccessTokens interface {
	VerifyAccessToken(ctx context.Context, raw string) (core.Actor, error)
}

// FactoryTokens authenticates the factory's own bearer tokens from a
// request; *auth.Service is one. It lets agents and services, which hold
// service tokens rather than OAuth grants, reach the same tools.
type FactoryTokens interface {
	Authenticate(r *http.Request) (core.Actor, error)
}

// factoryTokenPrefix starts every token the authentication service mints.
const factoryTokenPrefix = "sisf_"

// Config configures the endpoint.
type Config struct {
	Actions      Actions
	AccessTokens AccessTokens
	// FactoryTokens is optional; without it only OAuth access tokens work.
	FactoryTokens FactoryTokens
	// ResourceMetadataURL is the Protected Resource Metadata URL named in
	// WWW-Authenticate.
	ResourceMetadataURL string
	// Version is reported to clients; empty means "dev".
	Version string
	Logger  *slog.Logger
}

// NewHandler returns the MCP endpoint.
func NewHandler(cfg Config) (http.Handler, error) {
	switch {
	case cfg.Actions == nil || cfg.AccessTokens == nil:
		return nil, core.NewError(core.CodeInvalidArgument, "the MCP endpoint needs the action service and an access token verifier")
	case !strings.HasPrefix(cfg.ResourceMetadataURL, "https://") && !strings.HasPrefix(cfg.ResourceMetadataURL, "http://"):
		return nil, core.NewError(core.CodeInvalidArgument, "the MCP endpoint needs the absolute URL of its resource metadata")
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	srv := &server{cfg: cfg}
	streamable := sdk.NewStreamableHTTPHandler(srv.build, &sdk.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, Logger: cfg.Logger, MaxRequestBodyBytes: maxRequestBytes,
	})
	// Expiry is checked by the verifier against the factory's clock, so the
	// middleware's own wall-clock check is not used.
	bearer := auth.RequireBearerToken(srv.verify, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: cfg.ResourceMetadataURL, AllowMissingExpiration: true,
	})
	return bearer(streamable), nil
}

type server struct{ cfg Config }

// build returns the MCP server for one request, with a tool per action.
func (s *server) build(*http.Request) *sdk.Server {
	m := sdk.NewServer(&sdk.Implementation{Name: "sisf", Title: "Self-improving software factory", Version: s.cfg.Version},
		&sdk.ServerOptions{Logger: s.cfg.Logger,
			Instructions: "Every tool is a factory action. The factory checks your roles and scopes on every call; " +
				"a tool description never grants permission. Failed calls return typed errors with next_actions."})
	for _, spec := range s.cfg.Actions.Specs() {
		m.AddTool(toolFor(spec), s.call(spec.Name))
	}
	return m
}

// actorKey keys the verified actor in the token info the SDK hands to tools.
const actorKey = "sisf.actor"

// verify authenticates the bearer token: a factory token through the
// authentication service, anything else as an OAuth access token.
func (s *server) verify(ctx context.Context, token string, r *http.Request) (*auth.TokenInfo, error) {
	var (
		actor core.Actor
		err   error
	)
	if strings.HasPrefix(token, factoryTokenPrefix) && s.cfg.FactoryTokens != nil {
		actor, err = s.cfg.FactoryTokens.Authenticate(r)
	} else {
		actor, err = s.cfg.AccessTokens.VerifyAccessToken(ctx, token)
	}
	if err != nil {
		return nil, invalidToken(err)
	}
	actor.Surface = core.SurfaceMCP
	return &auth.TokenInfo{Scopes: actor.Scopes, UserID: actor.ID, Extra: map[string]any{actorKey: actor}}, nil
}

// actorOf returns the verified actor of a tool call.
func actorOf(req *sdk.CallToolRequest) (core.Actor, bool) {
	if req.Extra == nil || req.Extra.TokenInfo == nil {
		return core.Actor{}, false
	}
	actor, ok := req.Extra.TokenInfo.Extra[actorKey].(core.Actor)
	return actor, ok && actor.ID != ""
}
