# ADR 0005: Self-deploying Zone 1 behind independent QA and rollback

## Status

Status: accepted. Covers brief [§0](../self-improvement-software-factory-decision-brief.en.md) item 8, §6.1 and §6.2.

## Context

Automatic publishing is only safe if the thing judging the change is independent of the thing that made it, and if a bad release can be undone without trusting the new version.

## Decision

- Zone 1 self-publishes only after an independent QA agent passes it and the deterministic hard gates and rollback preflight pass. A high score never overrides a hard-gate failure.
- QA uses a different identity and workspace from the coding agent, and a different model or provider when two or more routes exist; with one route the receipt records `same_model_route: true` and policy may require Owner approval.
- The QA receipt is signed by the trusted evaluator and bound to the candidate digest, suite digest and baseline SHA. QA cannot score unbuilt code or reuse another commit's receipt.
- Release uses blue-green or canary with an observed revision and metrics, and rolls back automatically past the error threshold. Missing metrics, missing rollback or a broken adapter means hold, never assumed success.
- Rollback replaces compatible code and config only. It never overwrites customer data with an old snapshot.
- The score threshold and rollout numbers in the brief are starting values to calibrate, not proven.

## Consequences

- A backend without a safe version switch cannot self-publish.
- Low-traffic apps need a synthetic suite to self-publish.

## Acceptance test

- SISF-06: `internal/qa/self_scoring_rejected_test.go`.
- SISF-07: `internal/qa/hard_gate_overrides_score_test.go`.
- SISF-08: `internal/supervisor/release/publish_observed_revision_test.go`.
- SISF-09: `internal/supervisor/release/auto_rollback_restores_lkg_test.go`.
- SISF-20: `internal/supervisor/release/hold_without_metrics_or_rollback_test.go`.
