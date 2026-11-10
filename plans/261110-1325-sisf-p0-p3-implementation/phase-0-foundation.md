# Phase 0: Foundation (contracts, state machine, conformance, ADRs)

Branch `feat/p0-foundation` (base `main`). Brief refs: §0, §3, §5.2, §6.3, §10, §11, §12 P0, §15.

## Outcome

Every shared type and interface exists and compiles. The 15-state issue machine is enforced in code. `sisf conformance check` rejects a manifest that is missing a zone or breaks any of the four supervisor-boundary conditions (§6.3). Every §0 invariant and every §15 open point has an ADR. CI runs build, test and lint.

## Work packages

### P0-A contracts (strong, wave 0, runs alone)

- **Goal:** create `go.mod` (`module github.com/bestagentkits/sisf`, `go 1.24`) and every shared type and interface, so that later packages only add new files.
- **Owns (frozen after merge):**
  - `go.mod`, `go.sum` (after merge the orchestrator manages these; see plan.md "Contract freeze").
  - `pkg/core/`: `ids.go` (FactoryID, DeploymentID, RunID, IssueRef, ReleaseDigest, Zone), `states.go` (the 15 states and the events of §5.2), `record.go` (StateRecord with version, idempotency_key, lease_owner, fencing token, attempt, run_id, issue_id), `errors.go` (typed ErrorCode plus `next_actions`, §9 AX), `actions.go` (ActionSpec, Sensitivity, DryRun, Actor, the 7 roles of §7, the Authorizer interface), `policy.go` (PolicyRefs: policy/review/decision/feature_catalog SHA plus model_route), `budget.go` (BudgetMode, Reservation, the Gate interface), `receipt.go` (QAReceipt, every field in §6.1), `capabilities.go` (capability names, SupportLevel including `NOT_SUPPORTED` and `unverified`), `signer.go` (Signer and Verifier), `audit.go` (AuditEvent and the AuditLog interface), `clock.go`.
  - One interface file per package in `pkg/`: `agent/agent.go` (§3.1, with optional interfaces for resume, checkpoint, usage, humanInput, sessionLink, webhooks), `execution/execution.go` (§3.2, including the egress-enforcement capability), `deploy/deploy.go` (§3.3), `identity/identity.go` (login identity `issuer+sub`, kept separate from the repo-installation provider), `state/state.go` (Tx, CAS transition, lease with fencing, inbox dedup, outbox, budget ledger and reservation, a migration flag `backward_compatible`), `secrets/secrets.go` (SecretProvider, SecretRef, per-run Broker), `telemetry/telemetry.go` (the six required attributes), `github/github.go` (normalized WebhookEvent, Issue, Comment, Label, a PullRequest type that carries `IsFork` and `AuthorAssociation`, and Client), `qa/qa.go` (Evaluator, the rubric categories and weights, HardGate), `approvals/approvals.go` (subject_digest, nonce, expiry, policy revision, Store), `decision/decision.go` (Signal, Evidence, Verdict `ignore|observe|investigate|propose|implement|stop|collect_more_evidence`, Kind, the SystemOne interface), `manifest/manifest.go` (Go types for `factory.yaml`: factory_id, the three components, the nine units of §10, the supervisor block, the four networks, credentials and store role per zone, release, budget, egress, model routes).
- **Must not touch:** anything else.
- **Deps:** none. **Delivers:** the base for every package. Contract tests only, such as the enum completeness test for the 15 states.
- **Verify:** `go build ./... && go vet ./... && go test ./pkg/core/... ./pkg/...`

### P0-B state machine (strong, wave 1)

- **Goal:** `pkg/core/statemachine/` holds the transition table of §5.2 and the guard interfaces: Ready to Claimed needs a lease plus a budget reservation; QAEvaluation to ReleaseCandidate needs a valid receipt and every hard gate passing; Monitoring to Verified needs the observed revision plus the DoD; Closed can only follow Verified; Blocked to Ready needs an authorized retry. Do not add any state or edge that §5.2 does not have.
- **Owns:** `pkg/core/statemachine/**`. **Must not touch:** frozen files.
- **Deps:** P0-A. **Delivers:** the transition guards that later lifecycle and release tests rely on. A table test covers all 15x15 pairs, and a property test proves that Closed is unreachable without Verified.
- **Verify:** `go test ./pkg/core/statemachine/... -race`

### P0-C manifest loader, schema and conformance checker (strong, wave 1)

- **Goal:** load and validate `factory.yaml`. Publish its JSON Schema (§9 DX). Build the conformance checker, which:
  - rejects a manifest that is missing a zone or unit;
  - rejects any breach of the four supervisor-boundary conditions (separate pinned digest, an LKG/keys volume that Zone 1/2 cannot write, no pipeline permission over the supervisor digest, the startup self-check declared);
  - rejects a manifest whose declared zone isolation breaks security requirement 2 (shared credentials, shared store role, a Zone 1 write path into Zone 3);
  - checks that factory_id, deployment_id and runtime IDs can be told apart.

  Also provide `pkg/conformance/`, a per-interface self-grading kit and capability matrix that marks a backend lacking lifecycle, isolation, rollback or observability as `unsupported`. Add `cmd/sisf/main.go`, which sets up subcommand registration through one file per command, plus `cmd/sisf/cmd_conformance.go`.
- **Owns:** `pkg/manifest/` except `manifest.go`, `pkg/conformance/**`, `internal/conformance/**`, `cmd/sisf/main.go` (frozen after merge), `cmd/sisf/cmd_conformance.go`, `.sisf/factory.yaml` (the example instance), `schemas/factory.schema.json`.
- **Must not touch:** `docs/`, frozen files.
- **Deps:** P0-A. **Delivers:** SISF-16 (`internal/conformance/backend_capability_matrix_test.go`) and Done means 5 (`internal/conformance/supervisor_boundary_test.go`, `missing_zone_test.go`).
- **Verify:** `go test ./internal/conformance/... ./pkg/manifest/... ./pkg/conformance/...`, then `go run ./cmd/sisf conformance check .sisf/factory.yaml` must exit 0, and `go run ./cmd/sisf conformance check internal/conformance/testdata/missing-zone.yaml` must exit non-zero.

### P0-D ADRs, threat model and foundation docs (light, wave 1, writes no Go code except one docs test)

- **Goal:** write the ADRs below, `docs/threat-model.md` (assets, trust boundaries, abuse scenarios; each High scenario gets an ID so that a later test can map to it), `docs/capability-matrix.md`, `AGENTS.md`, `REVIEW.md`, `DECISIONS.md` (including the typed-question schema version), `FEATURES.md`, `CONTRIBUTING.md` (DCO). Change both brief files so they link to the ADRs instead of repeating the invariants, keeping EN and VI in step. Add a test that fails when the EN and VI briefs differ in section numbers, table IDs or SISF IDs.
- **Owns:** `docs/adr/**`, `docs/threat-model.md`, `docs/capability-matrix.md`, `docs/self-improvement-software-factory-decision-brief*.md`, `AGENTS.md`, `REVIEW.md`, `DECISIONS.md`, `FEATURES.md`, `CONTRIBUTING.md`, `tests/docs/brief_parity_test.go`.
- **Must not touch:** `README.md`, `docs/SECURITY.md` (the P3 docs package updates those), Go code outside `tests/docs/`.
- **Deps:** P0-A, which has to exist for the docs test to compile. **Delivers:** Done means 8, plus the P0 DoD for ADRs and EN/VI parity.
- **Verify:** `go test ./tests/docs/...`, and `ls docs/adr/*.md | wc -l` must report 18. The docs test also checks that each open-point ADR has `Status: proposed`.

### P0-E CI and tooling (light, wave 1, config only)

- **Goal:** one GitHub Actions workflow with jobs build (`go build ./...`), test (`go test ./... -race`) and lint (golangci-lint, pinned version), with `CGO_ENABLED=0`. Add `.golangci.yml`, plus a `Makefile` whose generic targets never need editing later: `build test lint e2e smoke up down`. `e2e` runs `go test -tags e2e ./tests/e2e/... -count=1 -timeout 45m`, and `smoke` runs `go test -tags smoke ./tests/smoke/... -count=1`.
- **Owns:** `.github/workflows/ci.yml`, `.golangci.yml`, `Makefile`. **Must not touch:** everything else.
- **Deps:** P0-A. **Verify:** `make build test lint`. CI passes on the P0 PR.

## Waves and integration order

| Wave | Packages (code-writing) | Non-code | Merge order |
|---|---|---|---|
| 0 | P0-A | none | A |
| 1 | P0-B, P0-C, P0-D (docs plus one test file) | P0-E | E, B, C, D |

After each merge, run `go build ./... && go test ./...`. Before the PR opens, run an isolated review (code-reviewer on the diff only).

## ADRs

Invariant ADRs (§0; each says which §0 item it covers):

| # | ADR | Covers |
|---|---|---|
| 0001 | three-zones-one-deployment | §0.2–0.5, §10 invariant |
| 0002 | trust-roots-and-supervisor-boundary | §0.9, §6.3 four conditions |
| 0003 | policy-precedence-and-foundation-files | §0.7, §4.1 |
| 0004 | github-ssot-and-operational-state | §0.6 |
| 0005 | self-deploy-zone1-qa-and-rollback | §0.8, §6.1–6.2 |
| 0006 | login-vs-repo-permission | §0.10, §7 |
| 0007 | budget-default-unlimited | §0.11, §8 |
| 0008 | owner-authority-features-upstream | §0.1, §0.12 |
| 0009 | tose-reuse-candidates-for-later-audit | §11 inheritance: list `delegation, decision, scope, budget, promotion, upstream` and the piadapter contract; no code imported |

Open-point ADRs (§15), all with status `proposed`:

| # | Open point | Recommended default |
|---|---|---|
| 0010 | New requirements placement | Adopt the mapping in plan.md: §3.5 goes to P2-A; §9 AX/UX/DX to P0-C, P1-E, P1-H, P3-B and P3-D3; the §7 extras per the security table; §10 units to P1-E and P2-H. |
| 0011 | v1 scope | Keep the §12 order. The early-v1-after-P2 proposal is not adopted; P3 stays in scope; revisit with value metrics after P2. |
| 0012 | Backup and restore | `state` and `artifacts` snapshots daily and before each release; RPO 24h or the last release, RTO 1h; rebuild from GitHub restores issues and SoW only, and ledger and audit loss is reported. Implementation waits for acceptance. |
| 0013 | Schema migration on self-update | Expand/contract migrations only, compatible across N and N-1. An incompatible migration needs Owner approval plus a pre-migration snapshot, and the migration hard gate blocks self-publish of it. |
| 0014 | Multiple repos | One factory serves one repo. More repos means more factory deployments; nothing (policy, budget, secrets) is shared. |
| 0015 | Provider limits | Per-provider concurrency cap plus a token bucket. GitHub rate-limit headers are honored with backoff. A provider outage pauses the run (never success) behind a circuit breaker. |
| 0016 | OSS governance | DCO sign-off, SemVer 0.x, `pkg/` interfaces marked experimental until 1.0, additive-only changes inside a minor version. |
| 0017 | Legal and data | Generated code falls under the repo license and the Owner is accountable. Only repo content and redacted telemetry go to providers; personal data is denied by default; no residency guarantee, stated openly. |
| 0018 | People during an incident | A generic webhook/email notifier outside GitHub, with the Owner as default on-call. If auto-rollback fails, use the kill switch (freeze plus hold) and the manual runbook `docs/guides/incident.md` (P3 docs). |

ADR numbers 0001–0018 make 18 files. Each one uses the sections Status, Context, Decision, Consequences, Acceptance test (or "deliberate non-goal").

## Phase verification

```bash
go build ./... && go test ./... -race && golangci-lint run
go run ./cmd/sisf conformance check .sisf/factory.yaml                                  # exit 0
for f in internal/conformance/testdata/reject-*.yaml; do ! go run ./cmd/sisf conformance check "$f" || exit 1; done
go test ./tests/docs/...
```

## Risks

| Risk | L x I | Mitigation |
|---|---|---|
| The contracts miss a field and later packages stall | M x H | Use the contract-change request protocol (plan.md). Prefer additive optional interfaces. Have P0-A read §3, §5.2, §6.1 and §10 in full. |
| A dependency has dropped Go 1.24 | M x M | Pin the newest version that still declares go ≤1.24, and check with `go mod tidy` in P0-A. |
| EN/VI briefs drift | L x M | The parity test runs in CI. |

## Rollback

P0 is greenfield. Revert = reset `feat/p0-foundation` to `main`. Each package is one squash commit, so `git revert <sha>` is enough for one package.
