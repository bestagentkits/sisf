# Phase 3: Identity, five surfaces, full Decision Layer, budgets, docs, real smoke run

Branch `feat/p3-surfaces-identity` (base `feat/p2-agents-qa-release`). Brief refs: §3.4 identity, §4, §5.3–5.4, §7, §8, §9, §12 P3.

## Outcome

GitHub and Google login, both against the fake OIDC, map to roles. Dashboard, REST, CLI, remote MCP (OAuth 2.1) and WebMCP all call one action service and get identical authorization. The Decision Layer uses the typed System One adapter (Jev/Clef), fails closed, and chooses do-nothing when evidence is thin. Budget is unlimited by default with install confirmation, or capped with reserve and stop. The docs no longer say "design stage". One real smoke run is recorded.

## Work packages

### P3-A identity, sessions, RBAC, fake OIDC (strong, wave 1)

- **Goal:**
  - `internal/auth`:
    - GitHub OAuth login and Google OIDC through `coreos/go-oidc` plus `x/oauth2`. Identity is keyed by `issuer+sub` and never by email alone. Signature, iss, aud, exp and nonce are validated.
    - HttpOnly SameSite session cookies plus CSRF.
    - Short-lived scoped PATs and service tokens.
    - Step-up reauthentication.
    - Revocation.
  - `internal/rbac`: the 7 roles, checked at action time from GitHub membership plus local grants. Labels never confer a role. The repo-installation mapping is separate from login.
  - `internal/testkit/fakeoidc`: Google-style OIDC with JWKS and nonce handling, plus GitHub-style OAuth. `sisf sim oidc` serves it on port 7468.
- **Owns:** `internal/auth/**`, `internal/rbac/**`, `pkg/identity/` except `identity.go`, `internal/testkit/fakeoidc/**`, `cmd/sisf/cmd_sim_oidc.go`.
- **Must not touch:** `internal/actions/`, frozen files.
- **Deps:** P2. **Delivers:** SISF-12 `internal/auth/sso_oauth_negative_test.go`. It covers a wrong issuer, audience or nonce, an expired token, an email-only match, a missing CSRF token, a revoked session, a cross-repo installation, and a step-up being required.
- **Verify:** `go test ./internal/auth/... ./internal/rbac/... ./internal/testkit/fakeoidc/...`

### P3-B action service, OpenAPI, llms.txt (strong, wave 1)

- **Goal:** `internal/actions` is one registry of `core.ActionSpec` with scopes, sensitivity and dry_run. Each domain package exposes `Actions()`, and only the integration package wires them.
  - Every action passes through `core.Authorizer`.
  - Sensitive writes go `plan -> approval -> execute -> audit`, with step-up.
  - Errors are typed and carry `next_actions`.
  - `runs.explain` reports why a run is stopped, with evidence.
  - Every action in the §9 query list is covered.
  - `go generate` produces `api/openapi.yaml` and `api/llms.txt` from the registry. A test fails when the generated files are stale.
- **Owns:** `internal/actions/**`, `api/**`.
- **Must not touch:** `internal/auth/`, `internal/control/`, frozen files.
- **Deps:** P2. **Delivers:** component tests for dry_run, the approval pipeline and generated-spec freshness.
- **Verify:** `go generate ./internal/actions/... && git diff --exit-code api/ && go test ./internal/actions/...`

### P3-E full Decision Layer, catalog actions (strong, wave 1)

- **Goal:**
  - `internal/decision/systemone`: a typed adapter for Jev and Clef. Each is a version-pinned HTTP JSON client configured in `factory.yaml`. A fake System One is used for tests. Errors, timeouts and schema violations fail closed. The adapter may only raise caution or priority, and deterministic checks keep their veto.
  - The typed questions are parsed from `DECISIONS.md` at the pinned SHA.
  - The P2 slice is extended into full goal proposals: options, a draft SoW and a measurable DoD.
  - `internal/catalog` gains `Actions()` for `features.list`, `changelog.list`, `docs.list` and `decisions.propose_update`. Any `decisions.propose_update` becomes a PR that needs Owner approval and is never applied directly.
- **Owns:** `internal/decision/**`, `internal/catalog/**`.
- **Must not touch:** `internal/policy/`, frozen files.
- **Deps:** P2-F. **Delivers:** component tests for fail-closed behavior, the do-nothing choice and proposal evidence.
- **Verify:** `go test ./internal/decision/... ./internal/catalog/...`

### P3-C budget and secrets surfaces (standard, wave 2)

- **Goal:** `internal/budget` implements `core.BudgetGate`.
  - `unlimited` is the default, and claims are refused until the Owner confirms at install time.
  - `alert_threshold` sends a notification without stopping.
  - `capped` reserves before a run starts, suspends on overrun, and supports a period reset and a rollback reserve.
  - Provider usage that cannot be measured is shown as `unverified`, and a provider without a hard cap is flagged.
  - Budget actions: `budget.get` and `budget.set`.
  - Secret-ref actions (refs only, never values), exposed by the secrets package through a new `Actions()`.
- **Owns:** `internal/budget/**`, `pkg/secrets/actions.go`.
- **Must not touch:** `internal/dispatcher/`, frozen files.
- **Deps:** P3-B. **Delivers:** SISF-14 `internal/budget/budget_modes_test.go`.
- **Verify:** `go test ./internal/budget/... ./pkg/secrets/...`

### P3-D1 REST and CLI surfaces (standard, wave 2)

- **Goal:**
  - `internal/control/api`: generic REST handlers driven by the registry, each with its OpenAPI path.
  - `sisf call <action>`, plus friendly aliases.
  - `sisf login`, using the device or browser verification flow (§5.4), with the result returned to the CLI.
- **Owns:** `internal/control/api/**`, `cmd/sisf/cmd_call.go`, `cmd/sisf/cmd_login.go`.
- **Must not touch:** `internal/control/*.go` root files, `ui/`, frozen files.
- **Deps:** P3-A, P3-B. **Verify:** `go test ./internal/control/api/... ./cmd/sisf/...`

### P3-D2 remote MCP with OAuth 2.1, WebMCP (strong, wave 2)

- **Goal:**
  - `internal/oauthas`: an OAuth 2.1 authorization server for MCP, with PKCE, Protected Resource Metadata, resource-bound audience and scopes, and issuer and audience validation. User login delegates to `internal/auth`.
  - `internal/mcp`: streamable-HTTP MCP that exposes registry actions. A tool description never grants permission.
  - `ui/static/webmcp.js`: feature-detects WebMCP and registers tools that call REST with the existing session plus CSRF. Where WebMCP is missing, it shows the fallback notice.
- **Owns:** `internal/oauthas/**`, `internal/mcp/**`, `ui/static/webmcp.js`.
- **Must not touch:** other `ui/` files, frozen files.
- **Deps:** P3-A, P3-B. **Verify:** `go test ./internal/oauthas/... ./internal/mcp/...`

### P3-D3 full dashboard and setup wizard (standard, wave 3)

- **Goal:** the seven pages of §9: Overview (stop and rollback buttons go through actions), Workboard, Run graph, Telemetry, Approvals (a one-line risk summary, the diff, the affected features, conditional approval), Product, and Security & finance. Add the setup wizard, which checks the GitHub App, SSO and budget, then runs a sample issue, and the Blocked explanation page.
- **Owns:** `ui/**` except `ui/static/webmcp.js`, `internal/control/web/**`.
- **Must not touch:** `internal/control/api/`, frozen files.
- **Deps:** P3-B, P3-C, P3-D1. **Verify:** `go test ./ui/... ./internal/control/web/...`

### P3-F docs (light, wave 3, no Go code)

- **Goal:**
  - Rewrite the README status: implemented through P3, with Standalone and Docker as supported modes and everything else experimental or unsupported.
  - Update `docs/SECURITY.md` (it still says "design stage").
  - Add `docs/guides/` for quickstart (both modes), operations, incident (the ADR 0018 runbook) and security.
  - Add `docs/acceptance-matrix.md`, which maps SISF IDs to tests.
  - Update the architecture HTML pages only where they claim nothing is implemented.
- **Owns:** `README.md`, `docs/SECURITY.md`, `docs/guides/**`, `docs/acceptance-matrix.md`, `docs/*.html`.
- **Must not touch:** `docs/adr/`, the brief files, code.
- **Deps:** none. **Verify:** `! grep -rni "design stage" README.md docs/`

### P3-G integration, five-surface parity, real smoke harness (strong, wave 4)

- **Goal:**
  - Wire auth, actions, MCP, OAuth AS, budget and dashboard into `cmd/factoryd` and the control server.
  - Add a `sim` profile that adds fake OIDC to compose.
  - Write the cross-surface acceptance tests.
  - Write the smoke test and its config.
- **Owns:** `cmd/factoryd/**`, `internal/control/*.go` (root files), `internal/testkit/harness/**`, `templates/**`, `build/**`, `tests/acceptance/five_surface_parity_test.go`, `tests/acceptance/cross_surface_approval_test.go`, `tests/e2e/**`, `tests/smoke/**`.
- **Must not touch:** files owned by P3-A to P3-F.
- **Deps:** all of P3. **Delivers:** SISF-13 `tests/acceptance/five_surface_parity_test.go`. The same action and actor run through REST, the CLI binary, the MCP client with an OAuth token, the WebMCP tool path (REST plus session plus CSRF) and a dashboard form POST, and must yield identical results and denials. The fallback is asserted when WebMCP is absent. The cross-surface approval test checks that the Owner can approve from any surface, that an Agent actor using an ambient Owner cookie is rejected, and that final human verification cannot be bypassed.
- **Verify:** see below.

## Waves and integration order

| Wave | Code-writing packages | Non-code | Merge order |
|---|---|---|---|
| 1 | P3-A, P3-B, P3-E | none | A, B, E |
| 2 | P3-C, P3-D1, P3-D2 | none | C, D1, D2 |
| 3 | P3-D3 | P3-F | F, D3 |
| 4 | P3-G | none | G |

## Phase verification (five-surface check)

```bash
go build ./... && go test ./... -race && golangci-lint run
go test ./tests/acceptance/... -count=1        # all 20 SISF tests, in-process fakes
make e2e                                        # compose + standalone full lifecycle with fake OIDC login via dashboard and CLI
go test ./tests/acceptance -run 'FiveSurface|CrossSurfaceApproval' -v
```

## Real smoke run (Done means 4)

This run is executed once by the orchestrator, after all of P3 merges into `feat/p3-surfaces-identity`. `make smoke` runs `tests/smoke/real_lifecycle_smoke_test.go` under tag `smoke`.

1. **Preflight**, which aborts with no side effect if anything fails:
   - `gh auth status` succeeds, and `gh repo view bestagentkits/sisf-sandbox` succeeds and shows the repo is private.
   - `claude --version` and `codex --version` both work, and the CLIs are logged in.
   - The tool versions are recorded.
2. **Setup:** Standalone mode, SQLite. GitHub uses the dev credential source `gh-cli` (`gh auth token`). This source is allowed only with `--dev` in Standalone, and doctor flags it as lower assurance because a real GitHub App is out of scope. GitHub is polled with reconciliation, because there is no public webhook. Coder route: claude. QA route: codex, so `same_model_route: false`. Policy C on the sandbox. `budget.mode: capped` with a small cap, plus `alert_threshold`. Short synthetic hold (2m). The sandbox is seeded with `examples/hello-app` if it is empty.
3. **Run:** the smoke operator opens one issue with labels `sisf:ready` and `kind:improvement`: "add GET /version returning the build revision". The factory runs the clarification step if it is needed, then SoW/DoD, claude patch and PR, codex QA receipt, local blue-green, synthetic hold, Verified, Closed.
4. **Assertions:**
   - The issue is closed by the factory.
   - There is one progress comment.
   - The PR is linked.
   - The receipt verifies.
   - The observed revision equals the candidate digest.
   - `/version` is served.
   - The usage ledger is non-empty.
   - Nothing touched any repo other than the sandbox.
5. **Report:** `plans/261110-1325-sisf-p0-p3-implementation/reports/real-smoke-run-report.md`, written by the orchestrator. It records versions, issue and PR URLs, run IDs, the receipt digest, timings, cost from the ledger, deviations and the teardown (`sisf down`). If the run fails, the report still records the result together with the evidence of the failure.

## Risks

| Risk | L x I | Mitigation |
|---|---|---|
| The Go MCP SDK API or its Go version floor does not fit Go 1.24 | M x M | Pin a compatible go-sdk. Fallback: a minimal JSON-RPC streamable-HTTP handler in `internal/mcp`. The decision is recorded in the PR. |
| No public Jev/Clef API spec | H x L | Ship a generic typed adapter plus the fake. Real endpoints are configured by the Owner later. Unknown output fails closed. |
| The real smoke run consumes subscription quota or flakes | M x M | Capped budget, one issue, a small task, a run timeout of 30m. Re-run only once, with Owner consent. |
| Five-surface drift | M x H | The parity test enumerates the registry, so a new action is untested only if it is missing from the registry. |

## Rollback

Reset to the `feat/p2-agents-qa-release` tip, or revert per package. For the smoke run, close the sandbox PR, delete the branch and run `sisf down -v`. Nothing outside `bestagentkits/sisf-sandbox` is touched.
