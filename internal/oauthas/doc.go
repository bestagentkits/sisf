// Package oauthas is the OAuth 2.1 authorization server that remote MCP
// clients use to act for a person who is signed in to the factory.
//
// Only the authorization code grant with PKCE (S256) exists: there is no
// implicit grant, no password grant, no plain PKCE and no client secret.
// Every client is public, whether listed in the configuration or added by
// dynamic registration, so registering a client buys nothing that PKCE does
// not already require. A redirect URI must match a registered one exactly.
//
// The person signs in through the factory's own session flow; this server
// never sees provider credentials. It reads the session through the
// authentication service, asks for consent on a page bound to that session,
// and issues a single-use code that lives for a minute.
//
// An access token is a short-lived JWT (RFC 9068) bound to one resource
// (RFC 8707): its audience is the MCP endpoint and nothing else. It carries
// the scopes the person granted, which are never more than the scopes their
// current roles allow, and the handle of the session it came from, so ending
// the session ends the token. Verification checks signature, type, issuer,
// audience, expiry and the session, at every request.
//
// The server publishes Authorization Server Metadata (RFC 8414) and the
// Protected Resource Metadata of the MCP endpoint (RFC 9728).
package oauthas
