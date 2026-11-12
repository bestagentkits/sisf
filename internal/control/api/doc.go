// Package api is the REST surface of the factory. One generic handler serves
// every action of the registry at the path the generated OpenAPI document
// declares, so a new action needs no new route and no surface gains power the
// action service does not give it.
//
// The actor of a call always comes from the authentication middleware, never
// from the request body. A cookie-authenticated write also carries the
// session's CSRF token (the middleware enforces it) and a bearer token never
// uses a cookie. Every failure is a typed error with next_actions; internal
// causes are logged for the operator and never sent to the caller.
//
// The package also serves the device verification flow behind "sisf login":
// the CLI starts a login request, a person approves it in a browser session,
// and the CLI collects a short-lived scoped token exactly once.
package api
