// Package control is the Zone 3 HTTP server: the read-only API, the minimal
// dashboard, the health endpoint and the GitHub webhook route.
//
// The API and the dashboard are two renderings of one Service. Both ask it
// for the same structs, so neither can show a state the other does not, and
// neither holds a power the other lacks.
//
// Trust rules the package enforces:
//   - every route except /healthz and the webhook route needs the bootstrap
//     Owner token, compared in constant time and never logged or rendered;
//   - a webhook is verified by the GitHub receiver and stored in the inbox
//     before it is acknowledged, and the inbox deduplicates on delivery ID;
//   - issue titles and comments are untrusted data: pages escape them and a
//     strict Content-Security-Policy backs that up;
//   - the server listens on loopback unless the configuration explicitly
//     allows another address.
package control
