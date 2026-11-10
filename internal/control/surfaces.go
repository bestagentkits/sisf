package control

import (
	"log/slog"
	"net/http"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/control/api"
	"github.com/bestagentkits/sisf/internal/control/web"
	"github.com/bestagentkits/sisf/internal/mcp"
	"github.com/bestagentkits/sisf/internal/oauthas"
	"github.com/bestagentkits/sisf/pkg/core"
)

// AuthPath is where the sign-in, step-up, session and token routes of the
// authentication service are mounted.
const AuthPath = "/auth/"

// Surfaces are the people- and agent-facing surfaces of the control zone.
// All of them call one action service, so none holds a power another lacks:
//
//   - REST at /api/v1/, which the sisf CLI also uses;
//   - the dashboard, whose forms post to /actions/{name};
//   - MCP at /mcp, for OAuth access tokens and the factory's own tokens;
//   - the WebMCP tool list at /webmcp/actions.json, which the dashboard's
//     script registers in the browser and calls back through REST with the
//     person's session and CSRF token;
//   - the OAuth 2.1 authorization server MCP clients get their tokens from;
//   - the sign-in, step-up and token routes under /auth/.
type Surfaces struct {
	// Identity authenticates every caller: sessions, personal access
	// tokens and service tokens. Its state is in memory, so a restart signs
	// everyone out and voids every token.
	Identity *auth.Service
	// Actions is the action registry behind every surface.
	Actions *actions.Service
	// OAuth issues the access tokens of MCP clients.
	OAuth *oauthas.Server
	// LoginProviders are the provider names the sign-in and step-up pages offer.
	LoginProviders []string
	// FormOrigins are the provider origins a step-up form may lead to, on
	// the dashboard and on the device verification page alike.
	FormOrigins []string
	// ExternalURL is the base URL people and clients reach the server at.
	ExternalURL string
	// Version is reported to MCP clients; empty means "dev".
	Version string
}

// mount builds every surface and routes it on mux.
func (s Surfaces) mount(mux *http.ServeMux, clock core.Clock, log *slog.Logger) error {
	if s.Identity == nil || s.Actions == nil || s.OAuth == nil || len(s.LoginProviders) == 0 {
		return core.NewError(core.CodeInvalidArgument,
			"the control server needs the authentication service, the action service, the authorization server and a login provider")
	}
	rest, err := api.New(api.Config{Actions: s.Actions, Identity: s.Identity, Clock: clock,
		ExternalURL: s.ExternalURL, LoginProviders: s.LoginProviders, FormOrigins: s.FormOrigins, Logger: log})
	if err != nil {
		return err
	}
	dashboard, err := web.New(web.Config{Actions: s.Actions, Identity: s.Identity, Clock: clock,
		LoginProviders: s.LoginProviders, FormOrigins: s.FormOrigins, Logger: log})
	if err != nil {
		return err
	}
	agents, err := mcp.NewHandler(mcp.Config{Actions: s.Actions, AccessTokens: s.OAuth, FactoryTokens: s.Identity,
		ResourceMetadataURL: s.OAuth.ProtectedResourceMetadataURL(), Version: s.Version, Logger: log})
	if err != nil {
		return err
	}
	mux.Handle(AuthPath, s.Identity.Handler())
	mux.Handle(api.MountPath, rest)
	mux.Handle(mcp.Path, agents)
	mux.Handle(mcp.ActionListPath, mcp.ActionListHandler(s.Actions))
	s.OAuth.Mount(mux)
	for _, path := range web.MountPaths() {
		mux.Handle(path, dashboard)
	}
	// Anything else is the dashboard's: it answers its own not-found page.
	mux.Handle("/", dashboard)
	return nil
}
