// Package web serves the full dashboard: the seven pages of the product
// brief, the setup wizard and the explanation page for a blocked run.
//
// A page holds no logic of its own. Every read and every write is one call
// to the action service with the dashboard surface and the signed-in actor,
// so authorization, step-up, approvals and typed errors are exactly those of
// the REST surface. A form POST needs the session and its CSRF token; a
// request without a session is sent to the sign-in page.
//
// An action the factory does not offer, or one that answers NOT_SUPPORTED,
// shows a "not available yet" state with its next actions; nothing is
// invented to fill a page.
package web
