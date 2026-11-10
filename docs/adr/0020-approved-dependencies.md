# ADR 0020: Approved dependencies

## Status

Status: accepted.

## Context

SISF is one Go module that builds without CGO. Every third-party module it imports runs inside the trusted zones: the control plane, the supervisor and the edge. A dependency is code the Owner has to trust, and its updates have to be reviewed. The set of modules therefore has to stay small and explicit, and an agent must never add one on its own initiative.

## Decision

These modules are approved. Each is pinned in `go.mod` at the version listed. A module that is not yet imported is pinned when it is adopted, at the newest release that still declares `go 1.25.x` (ADR 0019). The version below is that release as of this decision.

| Module | Pin | Use |
|--------|-----|-----|
| `modernc.org/sqlite` | v1.38.2 | Pure-Go SQLite for the Standalone state store. |
| `github.com/jackc/pgx/v5` | v5.12.0 | PostgreSQL driver for the Docker state store. |
| `gopkg.in/yaml.v3` | v3.0.1 | Factory manifest and policy files. |
| `github.com/santhosh-tekuri/jsonschema/v6` | v6.0.3 | Validating manifests and structured agent output against their schemas. |
| `go.opentelemetry.io/otel` (with `otel/trace`) | v1.46.0 | Tracing API. |
| `go.opentelemetry.io/otel/sdk` | v1.46.0 | Tracing SDK. v1.46.0 is the newest release that still declares Go 1.25. |
| `github.com/coreos/go-oidc/v3` | v3.21.0 (adopted) | OIDC sign-in of the dashboard (`internal/auth`). |
| `golang.org/x/oauth2` | v0.36.0 (adopted) | OAuth 2.0 flows for OIDC and GitHub (`internal/auth`). v0.37.0 and later declare Go 1.26. |
| `github.com/go-jose/go-jose/v4` | v4.1.9 (adopted) | JOSE and JWT signing and verification (`internal/oauthas`, `internal/testkit/fakeoidc`). |
| `github.com/modelcontextprotocol/go-sdk` | v1.8.0 (adopted) | The remote MCP server (`internal/mcp`) and the MCP client of the acceptance tests. |

The modules that an approved module requires are allowed as indirect dependencies, at the versions Go's minimal version selection picks. No package of an indirect dependency may be imported directly.

Any other dependency, whether direct or a new direct import of an indirect one, needs a new ADR before it is added. That ADR states why the standard library and the approved modules are not enough, and who reviews its updates. The same rule applies to replacing an approved module or to moving a pin to a release that requires a newer Go toolchain.

## Consequences

- `go.mod` changes only through a reviewed change that cites this ADR or a later one. An agent's candidate that adds a requirement is refused by the supply-chain gate unless an Owner has approved that dependency.
- Version updates within an approved module follow the toolchain rule of ADR 0019, and `govulncheck ./...` runs on every update.
- Adopting a module that is approved but not yet imported needs no new ADR, only its pin as recorded here or the newest release that still declares `go 1.25.x`.

## Acceptance test

`go.mod` requires no module that is neither listed here nor an indirect dependency of a listed module. `go build ./...` passes with `CGO_ENABLED=0`, and `govulncheck ./...` reports no affected code.
