# ADR 0006: Login identity is separate from repository permission

## Status

Status: accepted. Covers brief [§0](../self-improvement-software-factory-decision-brief.en.md) item 10 and §7.

## Context

Signing in with GitHub proves who a person is. A GitHub App installation grants the factory access to a repository. Mixing them lets a login imply repo power, or an email match imply identity.

## Decision

- Login identity is `issuer+sub` from GitHub or Google OIDC, with signature, issuer, audience, expiry and nonce validated. Email alone never establishes identity.
- Repository permission comes from a minimal GitHub App installation token and is modelled separately from login.
- There are seven roles: Owner, Maintainer, Reporter, Viewer, Agent, QA, Publisher/Deployment service. Roles are checked at action time; issue labels are never roles.
- Remote MCP uses OAuth 2.1 with resource-bound scopes. WebMCP is bound to the existing session and has no permission beyond the backend.
- Sensitive approvals are signed, single-use, bound to subject, release and policy, and expire. Who acted, human or agent, is audited.

## Consequences

- The identity and installation providers are separate interfaces.
- Every surface calls the same action service and cannot define its own permissions.

## Acceptance test

- SISF-12 (issuer, subject, audience, session, scopes, install mapping, step-up, revoke and CSRF negative tests): `internal/auth/sso_oauth_negative_test.go`, against a fake identity provider.
- SISF-13 (equivalent authorization on every surface): `tests/acceptance/five_surface_parity_test.go`, with `tests/acceptance/cross_surface_approval_test.go` and `tests/acceptance/step_up_and_write_limits_test.go`.
