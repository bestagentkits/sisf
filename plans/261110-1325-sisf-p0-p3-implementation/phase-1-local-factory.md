# Phase 1: Local and Compose three-zone factory, GitHub workflow

Branch `feat/p1-local-factory` (base `feat/p0-foundation`). Brief refs: §2, §3.4, §5.1–5.2, §5.4, §7 req 2 and 6, §10 runtime units and operating modes, §12 P1.

## Outcome

`sisf up --mode standalone` and `sisf up --mode docker` each start all three zones from a fresh clone. A labeled issue on the fake GitHub moves Intake, then Clarifying, then ScopeDraft, then Ready, then Claimed, then Developing (fake agent). Duplicate or reordered webhooks produce exactly one run. Killing the dispatcher and restarting it resumes the run. The API and the dashboard show the same state.

## Work packages

### P1-A state store and audit log (strong, wave 1)

- **Goal:** one SQL store implementation with two dialects: SQLite WAL through `modernc.org/sqlite` (Standalone) and PostgreSQL through `pgx/v5/stdlib` (Docker, §10). Versioned migrations carry the `backward_compatible` flag. The store provides CAS state transitions (version check), leases with monotonically increasing fencing tokens, a webhook inbox with delivery-ID dedup, an outbox, a budget ledger with reservations, and one store role per zone (Postgres GRANTs). The audit log is append-only and hash-chained: each record holds `prev_hash`, writes are INSERT-only, a Postgres trigger rejects UPDATE and DELETE, and `Verify()` detects edits.
- **Owns:** `pkg/state/` except `state.go`, `internal/audit/**`.
- **Must not touch:** frozen files, `internal/dispatcher/`.
- **Deps:** P0. **Delivers:** security req 6 (`internal/audit/hash_chain_tamper_test.go`). Every test runs against SQLite, and also against Postgres when `SISF_TEST_PG_DSN` is set.
- **Verify:** `go test ./pkg/state/... ./internal/audit/... -race`

### P1-B GitHub adapter and fake GitHub (standard, wave 1)

- **Goal:**
  - Webhook verification: HMAC SHA-256 accepted against any of the active secrets (so the secret can rotate), action/type, installation and repo checks.
  - Normalization to `github.WebhookEvent` and fast 2xx with persistence handled by the caller.
  - A bot-loop guard that ignores events caused by the factory's own label or comment edits.
  - A REST client for issues, comments, labels, PRs, checks and releases, with rate-limit backoff.
  - Polling and reconciliation through `since`/ETag.
  - A single progress comment, updated in place and found by a hidden marker.
  - `internal/testkit/fakegithub`, an httptest-compatible server that can also run standalone. It supports issues, labels, comments, PRs, webhook delivery with duplicate, reorder and drop injection, and a redeliver API.
  - `sisf sim github` serves the fake on port 7467.
- **Owns:** `pkg/github/` except `github.go`, `internal/testkit/fakegithub/**`, `cmd/sisf/cmd_sim_github.go`.
- **Must not touch:** `pkg/state/`, frozen files.
- **Deps:** P0. **Delivers:** component tests for HMAC (negative cases), dedup, loop guard and the single comment.
- **Verify:** `go test ./pkg/github/... ./internal/testkit/fakegithub/...`

### P1-C policy loader, intake and approvals core (strong, wave 1)

- **Goal:**
  - `internal/policy`: load `AGENTS.md`, `REVIEW.md`, `DECISIONS.md`, `FEATURES.md`/`features.yaml` and `.sisf/factory.yaml` only from the Owner-approved protected SHA. Apply the precedence order of §4.1. A conflict or an unapproved SHA yields hold.
  - `internal/intake`: label rules (`sisf:ready` is intake only; the `ai-handle` mapping is off by default; `agent:*` is a preference only; `deployment:<id>` is validated). Missing information produces a clarify comment, and an issue that times out goes to `needs-info` with no further spend. The SoW/DoD is versioned and attached with a digest. Scope classification sends a new feature or an out-of-delegation change to ApprovalPending.
  - `pkg/approvals` store: subject_digest, policy revision, nonce, expiry, single use, replay rejected. A significant SoW edit changes the digest and so invalidates the approval. Approvals are signed through `core.Signer`.
- **Owns:** `internal/policy/**`, `internal/intake/**`, `pkg/approvals/` except `approvals.go`.
- **Must not touch:** `pkg/github/`, `pkg/state/`, frozen files.
- **Deps:** P0. **Delivers:**
  - SISF-03 `internal/intake/unauthorized_and_new_feature_test.go`
  - SISF-11 `internal/policy/unapproved_policy_change_ignored_test.go`
- **Verify:** `go test ./internal/policy/... ./internal/intake/... ./pkg/approvals/...`

### P1-D dispatcher and fake agent (strong, wave 2)

- **Goal:** a single active dispatcher holds a lease. It claims a Ready issue atomically, with the lease and a budget reservation through `core.BudgetGate`. P1 wires a gate that refuses claims until the install-time budget confirmation exists (§8), and P3-C replaces it with the full implementation.
  - It enforces run timeout, max attempts and concurrency caps even in unlimited mode.
  - A fencing token is attached to every report, and stale reports are rejected.
  - On restart it resumes from state, and saved SoW, PR refs and artifacts survive.
  - `adapters/agent/fake` is a scripted `AgentBackend` (patch, fail, hang, crash) that P2 extends with the QA role.
- **Owns:** `internal/dispatcher/**`, `adapters/agent/fake/**`.
- **Must not touch:** `pkg/state/` (use it, do not edit it), frozen files.
- **Deps:** P1-A, P1-C. **Delivers:** SISF-05 `internal/dispatcher/worker_crash_fencing_test.go`.
- **Verify:** `go test ./internal/dispatcher/... ./adapters/agent/fake/... -race`

### P1-F control server and minimal dashboard (standard, wave 2)

- **Goal:** `internal/control` is the Zone 3 HTTP server. It serves `/healthz`, which aggregates the health of all three zones; a read-only `/api/v1/factory/status`, `/api/v1/issues` and `/api/v1/issues/{id}`, with typed errors; and the webhook route that hands to the inbox.
  - It listens on localhost by default and uses a bootstrap Owner token, generated at install, for the API. SSO comes in P3.
  - `ui/` is embedded `html/template` plus vanilla JS: read-only Overview and Workboard pages that render from the same service structs as the API.
- **Owns:** `internal/control/**`, `ui/**`. **Must not touch:** `cmd/factoryd/`, frozen files.
- **Deps:** P1-A, P1-B. **Delivers:** a component test `internal/control/api_dashboard_same_state_test.go` (P1 DoD).
- **Verify:** `go test ./internal/control/... ./ui/...`

### P1-G supervisor boundary and signing primitives (strong, wave 2)

- **Goal:** `internal/signing` holds ed25519 sign and verify for the release manifest and approvals, with a root public key from config. `cmd/supervisor` is a separate binary and image. At startup it checks its own digest against the signed manifest and refuses to run on a mismatch (§6.3 condition 4). It keeps the LKG record on a supervisor-only volume. It exposes a local admin socket for status. The supervisor does no release logic yet.
- **Owns:** `cmd/supervisor/**`, `internal/supervisor/**`, `internal/signing/**` (root files).
- **Must not touch:** frozen files, `templates/`.
- **Deps:** P0. **Delivers:** component tests `internal/supervisor/digest_self_check_test.go` and `internal/signing/manifest_signature_test.go`.
- **Verify:** `go test ./internal/supervisor/... ./internal/signing/...`

### P1-E packaging: Standalone and Docker modes, install, doctor, sample app (standard, wave 3)

- **Goal:**
  - `templates/compose/compose.yaml`: one project, one service per unit that exists so far (app, control, dispatcher, supervisor, state, edge, plus the artifacts volume). It has the four networks `app`, `factory`, `control` and `sandbox`. A socket proxy limited to container create and destroy on `sandbox` (never the raw socket in dispatcher, §10). Third-party images are pinned by digest, and own images are referenced by digest from the signed release manifest generated at `sisf up`. Each zone has its own Postgres role and credential. The `sim` profile runs fake GitHub.
  - `templates/standalone/`: launchd and systemd units, and the `sisf up --mode standalone` foreground supervisor of processes.
  - `internal/install`: init (factory_id, Owner token, root key in the Zone 3 secret dir with the lower-assurance warning, the unlimited budget confirm prompt plus `alert_threshold`), up, down, stop, restart, and doctor (three zones, GitHub permission, state store, rollback readiness).
  - `internal/edge` and `cmd/edge`: a plain reverse proxy.
  - `examples/hello-app`: the Zone 1 app with `/healthz`, `/metrics` and a writable data volume.
  - `build/*.Dockerfile`, all `CGO_ENABLED=0`.
  - Fixed ports: see plan.md.
- **Owns:** `templates/**`, `internal/install/**`, `internal/edge/**`, `cmd/edge/**`, `examples/hello-app/**`, `build/**`, `cmd/sisf/cmd_install.go`, `cmd/sisf/cmd_doctor.go`.
- **Must not touch:** `cmd/factoryd/`, `tests/`, frozen files.
- **Deps:** P1-F, P1-G (images), P1-A (roles). **Delivers:**
  - SISF-01 `internal/install/fresh_install_single_factory_test.go`
  - security req 2 `internal/install/zone_isolation_test.go`, which runs as a static check always and as a live check under tag `e2e`: Zone 1 cannot reach the control store role, and runner containers cannot read the Zone 2/3 secret mounts
- **Verify:** `go test ./internal/install/... ./internal/edge/... ./examples/...`, then `go run ./cmd/sisf conformance check` on the generated manifest.

### P1-H integration: factoryd wiring, harness, acceptance and e2e (strong, wave 3, merges last)

- **Goal:** `cmd/factoryd` gets the subcommands `control` and `dispatcher` and wires stores, adapters, intake and the gate. `internal/testkit/harness` is an in-process factory: fake GitHub, fake agent, SQLite in a tempdir, and a fake clock. Add the acceptance test and the e2e tests for both modes.
- **Owns:** `cmd/factoryd/**`, `internal/testkit/harness/**`, `tests/acceptance/duplicate_reordered_webhooks_test.go`, `tests/e2e/**`.
- **Must not touch:** every file owned by A–G. If one of them needs a fix, report it to the owning package's session.
- **Deps:** all of P1. **Delivers:** SISF-04 `tests/acceptance/duplicate_reordered_webhooks_test.go`, plus `tests/e2e/compose_lifecycle_test.go` and `tests/e2e/standalone_lifecycle_test.go`.
- **Verify:** see Phase verification.

## Waves and integration order

| Wave | Code-writing packages | Merge order |
|---|---|---|
| 1 | P1-A, P1-B, P1-C | A, B, C |
| 2 | P1-D, P1-F, P1-G | G, D, F |
| 3 | P1-E, P1-H (H starts in parallel and merges after E) | E, H |

## Phase verification

```bash
go build ./... && go test ./... -race && golangci-lint run
go run ./cmd/sisf up --mode docker --profile sim   # one command, all three zones
go run ./cmd/sisf doctor                           # all checks green
make e2e   # compose_lifecycle: fake-GH issue with sisf:ready -> clarify comment -> reply -> SoW v1 -> Ready -> Claimed -> Developing;
           # duplicate + reordered webhook => 1 run; docker kill dispatcher, restart => resumes and the stale report is fenced;
           # GET /api/v1/issues/{id}.state == state shown in dashboard HTML; standalone_lifecycle repeats this with processes + SQLite
go run ./cmd/sisf down
```

## Risks

| Risk | L x I | Mitigation |
|---|---|---|
| The SQLite and Postgres dialects diverge | M x M | One SQL layer, the same test suite on both, and a Postgres job in e2e. |
| The socket proxy allows too much | L x H | Pin a known image by digest. The static test asserts the allowed endpoints, and the live test tries a forbidden create. |
| P1-H blocked by fixes needed in other packages | M x M | H reports to the owning session and does not edit the file itself. The orchestrator re-dispatches a small fix to the owner. |

## Rollback

Reset `feat/p1-local-factory` to the `feat/p0-foundation` tip, or revert one package commit at a time. `sisf down -v` removes the local volumes from the test runs. No external data is touched; fake GitHub only.
