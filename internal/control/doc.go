// Package control is the Zone 3 HTTP server: the dashboard, the REST API,
// the MCP endpoint and its OAuth authorization server, the WebMCP tool
// list, the sign-in routes, the health endpoint, the GitHub webhook route
// and the public bug report route.
//
// Every surface calls one action service, so none can show a state another
// does not, and none holds a power another lacks.
//
// Trust rules the package enforces:
//   - every route except /healthz, the webhook route, the report route and
//     the static assets authenticates its caller through the authentication
//     service: a browser session, a personal access token, a service token
//     or an OAuth access token; roles are resolved per call, never taken
//     from the caller;
//   - a webhook is verified by the GitHub receiver and stored in the inbox
//     before it is acknowledged, and the inbox deduplicates on delivery ID;
//   - issue titles and comments are untrusted data: pages escape them and a
//     strict Content-Security-Policy backs that up;
//   - the server listens on loopback unless the configuration explicitly
//     allows another address.
package control
