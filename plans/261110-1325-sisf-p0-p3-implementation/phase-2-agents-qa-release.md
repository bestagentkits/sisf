# Phase 2: Built-in agents, independent QA, release with rollback, Decision Layer slice

Branch `feat/p2-agents-qa-release` (base `feat/p1-local-factory`). Brief refs: §3.1–3.3, §3.5, §4.2, §6, §7 req 1, 3, 4, 5, 7, 8, §8, §10 Docker constraints, §12 P2.

## Outcome

Agent A (Claude CLI adapter, fake in tests) writes a patch, and agent B (Codex adapter, fake in tests) runs QA with a different identity and model route. A signed `qa_receipt` gates release. The supervisor stages the candidate, previews it, runs a preflight rollback, rolls out by blue-green or canary 5→25→100 through `edge`, observes the revision, and either promotes or restores the LKG automatically. The issue closes only after Verified. The factory updates a read-only Zone 3 page by itself and rolls it back. One telemetry signal produces exactly one proposal, or nothing when the evidence is thin.

## Work packages

### P2-A built-in agent adapters and model routing (standard, wave 1)

- **Goal:** `adapters/agent/claude` and `adapters/agent/codex` wrap the local CLIs (claude in print/stream-JSON mode; codex in exec or app-server mode) behind `AgentBackend`.
  - Declare capabilities, and return `NOT_SUPPORTED` honestly for anything missing.
  - Provider tokens never go into logs.
  - `internal/agentrun` implements §3.5: code-chosen routes per step type from `factory.yaml`, the escalation ladder (verify first, then more effort, then a larger model), cheap first with only QA failures re-run, model switches only at a state boundary, and a structured handoff summary (SoW, DoD, decisions, evidence) instead of chat history.
  - The CLIs are exercised through recorded fixtures and a stub binary on `PATH`.
- **Owns:** `adapters/agent/claude/**`, `adapters/agent/codex/**`, `internal/agentrun/**`, `pkg/agent/` except `agent.go`.
- **Must not touch:** `adapters/agent/fake/`, frozen files.
- **Deps:** P1. **Delivers:** SISF-18 `internal/agentrun/fresh_agent_context_rebuild_test.go`.
- **Verify:** `go test ./adapters/agent/... ./internal/agentrun/... ./pkg/agent/...`

### P2-B execution sandboxes, egress and secret broker (strong, wave 1)

- **Goal:**
  - `adapters/exec/local`: trusted code only; refuses untrusted code in automatic mode.
  - `adapters/exec/docker`: ephemeral runner and qa-runner containers on `sandbox`, created only through the socket proxy, with no inbound connections. It runs with read-only rootfs, cpu, mem and pids limits and a time limit, and uses gVisor `runsc` when the host has it.
  - `internal/egress`: a CONNECT/HTTP allowlist proxy. Its default list matches §7 req 1. Blocked domains are logged for Owner review. The policy changes only through `plan -> approval -> execute -> audit`. An Agent-role actor cannot widen it, and widening or turning off blocking returns a risk warning. The egress capability is published in `capabilities`.
  - `internal/secretbroker`: per-run secrets limited by purpose, scope and TTL, revoked on cancel; the GitHub App and Owner tokens are never exposed to a runner.
- **Owns:** `adapters/exec/local/**`, `adapters/exec/docker/**`, `internal/egress/**`, `internal/secretbroker/**`, `pkg/execution/` except `execution.go`, `tests/acceptance/prompt_injection_containment_test.go`.
- **Must not touch:** `templates/`, `cmd/edge/`, frozen files.
- **Deps:** P1. **Delivers:**
  - security req 1 `internal/egress/allowlist_policy_test.go`
  - SISF-19 `tests/acceptance/prompt_injection_containment_test.go`, using the P1 harness read-only: an injected issue body attempts token exfiltration, a forbidden domain, and writes to the QA baseline and policy; every attempt must fail
- **Verify:** `go test ./adapters/exec/... ./internal/egress/... ./internal/secretbroker/... ./tests/acceptance/ -run PromptInjection`. Docker cases run under `-tags e2e`.

### P2-C delegated signing keys, local secret store, supply chain (strong, wave 1)

- **Goal:**
  - `internal/signing/delegation`: the root key stays offline with the Owner. It issues short-lived delegated keys (purpose, not_after, signed by the root) that only the supervisor and the approval service can load. Revocation works without replacing the root. Webhook-secret rotation uses an overlap window with no event loss. The local profile shows a lower-assurance flag.
  - `pkg/secrets/local`: secrets encrypted at rest (AES-GCM, key from the Zone 3 secret dir).
  - `internal/supplychain`:
    - SBOM: CycloneDX JSON generated from `go version -m` and the image digest.
    - Provenance statement.
    - Artifact signing and verification.
    - The dependency, lockfile, base-image and CI-config change detector, exposed as a `qa.HardGate`. Pins are required, govulncheck must be clean, and a new dependency needs Owner approval.
- **Owns:** `internal/signing/delegation/**`, `pkg/secrets/local/**`, `internal/supplychain/**`.
- **Must not touch:** `internal/signing/*.go` root files, `internal/supervisor/`, frozen files.
- **Deps:** P1-G. **Delivers:**
  - security req 3 `internal/supplychain/unsigned_artifact_rejected_test.go`
  - security req 5 `internal/signing/delegation/delegated_key_revocation_test.go`, plus `webhook_secret_rotation_test.go`
- **Verify:** `go test ./internal/signing/... ./pkg/secrets/... ./internal/supplychain/...`

### P2-D independent QA and receipts (strong, wave 2)

- **Goal:** `internal/qa` holds:
  - the rubric 30/25/20/15/10 with threshold 85 read from `REVIEW.md` at the pinned SHA;
  - the hard gates of §6.1, plus the supply-chain gate from P2-C;
  - a read-only baseline locked to the commit and policy SHA;
  - the rule that QA identity and workspace differ from the coder's;
  - a different model route when two or more exist, and otherwise `same_model_route: true` plus the policy action;
  - a refusal to score an unbuilt candidate;
  - a `qa_receipt` signed with the delegated key, holding every §6.1 field, expiring, and bound to the candidate digest. A receipt for another commit is rejected.

  Extend `adapters/agent/fake` with a QA role.
- **Owns:** `internal/qa/**`, `pkg/qa/` except `qa.go`, `adapters/agent/fake/**`.
- **Must not touch:** `internal/supervisor/`, frozen files.
- **Deps:** P2-A, P2-C. **Delivers:**
  - SISF-06 `internal/qa/self_scoring_rejected_test.go`
  - SISF-07 `internal/qa/hard_gate_overrides_score_test.go`
- **Verify:** `go test ./internal/qa/... ./pkg/qa/... ./adapters/agent/fake/...`

### P2-E deployment adapters, edge traffic split, sample-app release hooks (standard, wave 2)

- **Goal:**
  - `adapters/deploy/compose`: a `DeploymentBackend` through a supervisor-only socket proxy. It implements stage, preview, measure, promote, observedRevision (read from the running container label and the `/healthz` revision, never from the API call result) and rollback. Capabilities declare blue-green and canary.
  - `adapters/deploy/local`: process blue-green on ports 7461/7462 for Standalone.
  - `internal/edge`: weighted split between stable and canary, a revision header, and per-revision request, 5xx and sample counters.
  - `cmd/edge`: wires in the egress listener from `internal/egress`.
  - `examples/hello-app`: an env-driven fault injection, a `revision` field, and the `synthetic/` suite for low-traffic mode.
- **Owns:** `adapters/deploy/compose/**`, `adapters/deploy/local/**`, `pkg/deploy/` except `deploy.go`, `internal/edge/**`, `cmd/edge/**`, `examples/hello-app/**`.
- **Must not touch:** `internal/supervisor/`, `templates/`, frozen files.
- **Deps:** P2-B (egress package). **Delivers:** component tests for observed revision and the split; no SISF ID.
- **Verify:** `go test ./adapters/deploy/... ./internal/edge/... ./examples/...`

### P2-F telemetry, redaction, Decision Layer slice, untrusted operational data (strong, wave 2)

- **Goal:**
  - `pkg/telemetry` implementation: OTel spans plus normalized events carrying the six required attributes.
  - `internal/redact`: secrets and personal data redacted before anything reaches logs, transcripts, issues or PRs, with retention per data kind and a provider data-sharing policy.
  - `internal/decision`: the deterministic slice of §4.2. The signal is the 5xx rate per endpoint from edge counters. The flow is data quality, baseline, typed questions, evidence, scope and policy, proposal. Proposals are deduplicated by signal fingerprint. Too few samples yields `collect_more_evidence` or `ignore`, and no issue is created.
  - `internal/intake/report`: the public report endpoint with rate limit, size limit and dedup; content is stored as data only.
  - `internal/intake/prgate`: fork PRs run sandboxed with no secrets until a Maintainer accepts them; private repos allow collaborators only.
- **Owns:** `pkg/telemetry/` except `telemetry.go`, `internal/redact/**`, `internal/decision/**`, `internal/intake/report/**`, `internal/intake/prgate/**`.
- **Must not touch:** other `internal/intake/` files, frozen files.
- **Deps:** P1. **Delivers:**
  - SISF-17 `internal/decision/no_benefit_signal_ignored_test.go`
  - security req 4 `internal/decision/untrusted_operational_data_test.go`, which also covers the report limits and the fork-PR gate
  - security req 7 `internal/redact/redaction_retention_test.go`
- **Verify:** `go test ./pkg/telemetry/... ./internal/redact/... ./internal/decision/... ./internal/intake/...`

### P2-G release supervisor, monitor, kill switch, catalog (strong, wave 3)

- **Goal:** `internal/supervisor/release` runs the pipeline in §6.2:
  - verify the artifact signature and the receipt;
  - preflight rollback, which holds when there is no LKG, no metrics, or a broken adapter;
  - blue-green, or canary 5→25→100 when the adapter can split traffic;
  - a monitor that rolls back after `min_samples` over a window when both 5xx > threshold and delta > 1pp hold for 2 consecutive windows; critical health triggers an immediate rollback;
  - low-traffic synthetic hold;
  - restore the LKG and leave data volumes untouched; the issue goes to RolledBack then Blocked with a recovery report;
  - promote, then Verified, then Closed with a changelog and catalog update done by the Publisher identity;
  - refuse a candidate that touches the supervisor, keys, policy enforcement, QA baseline or rollback rules (a governance change).

  `internal/supervisor/killswitch` and `sisf emergency-stop` speak directly to the supervisor socket and the state store. They stop claims, cancel runs, revoke per-run secrets and freeze publishing, with no dependency on control or dispatcher. `internal/catalog` holds `FEATURES.md`/`features.yaml` and `CHANGELOG.md` writes through PRs.
- **Owns:** `internal/supervisor/**`, `cmd/supervisor/**`, `internal/catalog/**`, `cmd/sisf/cmd_emergency_stop.go`, `tests/security/threat_model_coverage_test.go`.
- **Must not touch:** `internal/qa/`, `adapters/deploy/`, frozen files.
- **Deps:** P2-C, P2-D, P2-E, P2-F. **Delivers:**
  - SISF-08 `internal/supervisor/release/publish_observed_revision_test.go`
  - SISF-09 `internal/supervisor/release/auto_rollback_restores_lkg_test.go`
  - SISF-20 `internal/supervisor/release/hold_without_metrics_or_rollback_test.go`
  - security req 8 `internal/supervisor/killswitch/emergency_stop_test.go` and `tests/security/threat_model_coverage_test.go` (every High scenario in `docs/threat-model.md` maps to an existing test)
- **Verify:** `go test ./internal/supervisor/... ./internal/catalog/... ./tests/security/...`

### P2-H integration: wiring, full lifecycle, self-update, Compose topology (strong, wave 4)

- **Goal:**
  - Wire everything into `cmd/factoryd`.
  - Add the runner and qa-runner images, the supervisor socket proxy, the egress route and the sandbox network rules to the compose and standalone templates.
  - Mount the report route in control.
  - Extend the harness.
  - Self-update: the fake agent patches a read-only dashboard page. The supervisor builds a new `control` image, releases it, and rolls it back on an injected fault. Zone 1 must keep serving the whole time (a continuous probe). The supervisor digest must be unchanged.
- **Owns:** `cmd/factoryd/**`, `internal/testkit/harness/**`, `templates/**`, `build/**`, `internal/control/*.go` (root files only), `tests/acceptance/issue_full_lifecycle_test.go`, `tests/acceptance/factory_self_update_zone3_test.go`, `tests/acceptance/two_factories_same_upstream_test.go`, `tests/e2e/**`.
- **Must not touch:** files owned by P2-A to P2-G. Report needed fixes instead of editing.
- **Deps:** all of P2. **Delivers:**
  - SISF-02 `tests/acceptance/issue_full_lifecycle_test.go`
  - SISF-10 `tests/acceptance/factory_self_update_zone3_test.go`
  - SISF-15 `tests/acceptance/two_factories_same_upstream_test.go`: two harness factories with different factory_ids on one fake upstream. A's improvement lands only in A's state and branch; B is unchanged; the upstream contribution action returns an approval-required error unless the Owner triggers it.
- **Verify:** see below.

## Waves and integration order

| Wave | Code-writing packages | Merge order |
|---|---|---|
| 1 | P2-A, P2-B, P2-C | C, B, A |
| 2 | P2-D, P2-E, P2-F | E, F, D |
| 3 | P2-G | G |
| 4 | P2-H | H |

## Phase verification (including the canary and rollback check)

```bash
go build ./... && go test ./... -race && golangci-lint run
make e2e    # adds to tests/e2e:
  # compose_full_lifecycle: sample issue -> ... -> QA receipt -> canary 5/25/100 -> Monitoring -> Verified -> Closed; GH + dashboard milestones match
  # compose_canary_rollback: fault-injected candidate under synthetic load (>= min_samples) -> rollback to LKG within 2 windows;
  #   observed revision == LKG digest; issue Blocked with evidence comment; sha256 of hello-app data volume unchanged
  # compose_hold_no_metrics: metrics endpoint disabled -> release holds, never promotes
  # compose_self_update_zone3: control image swapped and rolled back; app probe 0 failures; supervisor digest unchanged
  # standalone_full_lifecycle: same lifecycle with deploy/local blue-green
```

## Risks

| Risk | L x I | Mitigation |
|---|---|---|
| The claude or codex CLI output format changes | M x M | Adapters parse stream JSON by type and tolerate unknown fields. Fixtures are recorded from the installed versions, and versions are logged in the run record. |
| Flaky timing in canary e2e | M x M | A fake clock in acceptance tests. In e2e, the load generator and window sizes come from test config (short windows) and production defaults stay as documented. |
| gVisor missing on macOS | H x L | Optional per §10. The capability is reported, and the docker runc path is tested. |
| P2 scope is the largest | H x M | Four waves with strong-tier packages kept narrow. G and H are serialized on purpose. |

## Rollback

Reset to the `feat/p1-local-factory` tip, or revert per package. The release paths only act on local Compose and Standalone instances. `sisf down -v` clears them.
