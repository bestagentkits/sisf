---
title: "SISF P0–P3 implementation"
description: "Go implementation of the SISF decision brief, phases P0–P3, as four stacked PRs with dispatchable work packages."
status: pending
priority: P1
effort: 140h
branch: feat/p0-foundation
tags: [sisf, go, factory, stacked-prs]
created: 2026-11-10
---

# SISF P0–P3 implementation plan

Contract: [decision brief §0–§15](../../docs/self-improvement-software-factory-decision-brief.en.md). The Done means, scope and defaults are those confirmed in `orchestrator-state.md`; this plan does not change them.

## Phases

| Phase | Branch (base) | Waves | Packages | File |
|---|---|---|---|---|
| P0 Foundation | feat/p0-foundation (main) | 2 | A–E | [phase-0-foundation.md](phase-0-foundation.md) |
| P1 Local factory | feat/p1-local-factory (P0) | 3 | A–H | [phase-1-local-factory.md](phase-1-local-factory.md) |
| P2 Agents, QA, release | feat/p2-agents-qa-release (P1) | 4 | A–H | [phase-2-agents-qa-release.md](phase-2-agents-qa-release.md) |
| P3 Surfaces, identity | feat/p3-surfaces-identity (P2) | 4 | A–G | [phase-3-surfaces-identity.md](phase-3-surfaces-identity.md) |

Each phase depends on the previous phase being fully merged into its branch. Inside a phase, the order is set by the wave tables. There are never more than 3 concurrent code-writing packages, each in its own worktree.

## Dispatch rules (apply to every package)

- **Ownership:** a package edits only the paths it owns. Paths are disjoint within a wave. A later phase may edit paths from an earlier phase only through the package that owns them in that phase.
- **Contract freeze:** after P0-A merges, these are frozen:
  - every file P0-A created (`internal/core/*.go` and one interface file per `pkg/*/`);
  - `cmd/sisf/main.go` from P0-C;
  - `Makefile` and `.github/workflows/ci.yml` from P0-E.

  New CLI commands go in new files named `cmd/sisf/cmd_<area>.go`, which register themselves through `init()`.
- **Contract change request:** a package that needs a frozen file changed stops editing it. It ends with `Status: NEEDS_CONTEXT` and a block `CONTRACT CHANGE REQUEST: file, symbol, proposed Go signature, reason, affected packages`. The orchestrator batches requests at the wave boundary, applies them in one strong-tier "contracts amend" commit (additive changes preferred), runs `go build ./... && go test ./...`, then re-dispatches.
- **go.mod/go.sum:** the orchestrator owns these. A package may `go get` only the approved dependencies: `modernc.org/sqlite`, `github.com/jackc/pgx/v5`, `gopkg.in/yaml.v3`, `github.com/santhosh-tekuri/jsonschema/v6`, `go.opentelemetry.io/otel` (plus sdk), `github.com/coreos/go-oidc/v3`, `golang.org/x/oauth2`, `github.com/go-jose/go-jose/v4`, `github.com/modelcontextprotocol/go-sdk`. govulncheck runs through `go run golang.org/x/vuln/cmd/govulncheck@<pinned>`. Any other dependency is a contract change request. Merge conflicts are resolved with `go mod tidy`. Pin versions that support Go 1.24.
- **Tests first.** Every security and spending rule gets a negative test. Test names and code comments describe behavior; they never contain plan, phase or package IDs. Acceptance tests are traceable only through the tables below.
- **Test layers:**
  - unit and component: inside the owning package;
  - acceptance: `tests/acceptance/` with the in-process harness and the fakes;
  - e2e: `tests/e2e/` under tag `e2e`, on Compose and Standalone;
  - real smoke: `tests/smoke/` under tag `smoke`.
- **Ports:**

  | Port | Use |
  |---|---|
  | 7460 | edge (app public) |
  | 7461 / 7462 | app blue / green |
  | 7463 | control (dashboard, API, MCP, webhook) |
  | 7464 | supervisor admin (localhost only) |
  | 7465 | dispatcher health |
  | 7466 | egress proxy |
  | 7467 | fake GitHub |
  | 7468 | fake OIDC |
  | 7469 | Postgres debug (host-bound only with `--debug`) |

- **Per package done:** its verify command passes, `go build ./... && go test ./...` passes on the wave branch, and the status block is returned.
- **Per phase done:** the phase verification passes, an isolated review of the diff is clean, CI is green, and the PR is opened on the previous branch.

## Acceptance tests SISF-01…20 (each appears in exactly one package)

| ID | Phase / package | Test file |
|---|---|---|
| SISF-01 | P1-E | internal/install/fresh_install_single_factory_test.go |
| SISF-02 | P2-H | tests/acceptance/issue_full_lifecycle_test.go |
| SISF-03 | P1-C | internal/intake/unauthorized_and_new_feature_test.go |
| SISF-04 | P1-H | tests/acceptance/duplicate_reordered_webhooks_test.go |
| SISF-05 | P1-D | internal/dispatcher/worker_crash_fencing_test.go |
| SISF-06 | P2-D | internal/qa/self_scoring_rejected_test.go |
| SISF-07 | P2-D | internal/qa/hard_gate_overrides_score_test.go |
| SISF-08 | P2-G | internal/supervisor/release/publish_observed_revision_test.go |
| SISF-09 | P2-G | internal/supervisor/release/auto_rollback_restores_lkg_test.go |
| SISF-10 | P2-H | tests/acceptance/factory_self_update_zone3_test.go |
| SISF-11 | P1-C | internal/policy/unapproved_policy_change_ignored_test.go |
| SISF-12 | P3-A | internal/auth/sso_oauth_negative_test.go |
| SISF-13 | P3-G | tests/acceptance/five_surface_parity_test.go |
| SISF-14 | P3-C | internal/budget/budget_modes_test.go |
| SISF-15 | P2-H | tests/acceptance/two_factories_same_upstream_test.go |
| SISF-16 | P0-C | internal/conformance/backend_capability_matrix_test.go |
| SISF-17 | P2-F | internal/decision/no_benefit_signal_ignored_test.go |
| SISF-18 | P2-A | internal/agentrun/fresh_agent_context_rebuild_test.go |
| SISF-19 | P2-B | tests/acceptance/prompt_injection_containment_test.go |
| SISF-20 | P2-G | internal/supervisor/release/hold_without_metrics_or_rollback_test.go |

## Additional security requirements, §7 (each appears in exactly one package)

| # | Requirement | Phase / package | Test file |
|---|---|---|---|
| 1 | Agent egress | P2-B | internal/egress/allowlist_policy_test.go |
| 2 | Isolation between the zones | P1-E | internal/install/zone_isolation_test.go (static, plus live under `e2e`) |
| 3 | Supply chain | P2-C | internal/supplychain/unsigned_artifact_rejected_test.go |
| 4 | Operational data is untrusted | P2-F | internal/decision/untrusted_operational_data_test.go |
| 5 | Signing-key management | P2-C | internal/signing/delegation/delegated_key_revocation_test.go, webhook_secret_rotation_test.go |
| 6 | Tamper-evident audit log | P1-A | internal/audit/hash_chain_tamper_test.go |
| 7 | Redaction and retention | P2-F | internal/redact/redaction_retention_test.go |
| 8 | Kill switch and threat model | P2-G | internal/supervisor/killswitch/emergency_stop_test.go, tests/security/threat_model_coverage_test.go |

Requirements 1–3 land before untrusted code first runs (P2-B and P2-D). Requirement 6 lands in P1, earlier than the brief requires, because approvals already audit there. `docs/threat-model.md` is written in P0-D, and requirement 8's coverage test checks it.

## Done means: where each item is proven

| # | Proof |
|---|---|
| 1 | CI on `feat/p3-surfaces-identity`: build, test and lint |
| 2 | `go test ./tests/acceptance/... ./internal/... -count=1` covers the 20 files above |
| 3 | `sisf up --mode docker --profile sim` + `make e2e` (compose_full_lifecycle), from P2-H onward |
| 4 | Real smoke run ([phase-3](phase-3-surfaces-identity.md#real-smoke-run-done-means-4)), with the report in `reports/` |
| 5 | P0-C conformance tests and CLI exit codes |
| 6 | Security table above |
| 7 | e2e `standalone_*` and `compose_*` tests, P1-H and P2-H |
| 8 | ADRs 0010–0018 ([phase-0](phase-0-foundation.md#adrs)) |
| 9 | Four stacked PRs, each with CI green plus an isolated review |
| 10 | P3-F, verified with `grep` |

## Backwards compatibility and rollback

This is a greenfield repo with no existing users or data. The `pkg/` interfaces are versioned 0.x per ADR 0016. Each package merges as one squash commit, so any package can be reverted. A phase rolls back by resetting its branch to the parent tip. Nothing touches production. Only `bestagentkits/sisf-sandbox` is touched, once, during the smoke run.

## Top risks (full lists in the phase files)

| Risk | Mitigation |
|---|---|
| A contract gap stalls parallel packages | P0-A runs alone on the strong tier, and the batched contract-change protocol applies |
| P2 scope and timing flakiness | Fake clock in acceptance tests; short windows in e2e config; G and H serialized |
| Dependencies drop Go 1.24 | Pin older compatible versions; anything else becomes a contract change request |
| The real smoke run burns quota | Capped budget, a single small issue, a 30m timeout |

## Unresolved questions

1. Go 1.24 is past upstream support in Nov 2026. Should we keep it, as fixed, or move to 1.25+ if a dependency forces it?
2. Docker mode uses PostgreSQL per §10, while P1 in §12 says SQLite. The plan supports both (SQLite for Standalone). Confirm.
3. The smoke run uses `gh auth token` as a dev-only credential because a real GitHub App is out of scope. Confirm that this is acceptable for the sandbox only.
