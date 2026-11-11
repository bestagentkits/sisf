# ADR 0004: GitHub as source of truth, database for operational state

## Status

Status: accepted. Covers brief [§0](../self-improvement-software-factory-decision-brief.en.md) item 6.

## Context

GitHub is the natural home for source, docs, decisions and work items. It is a poor transactional store: webhooks arrive late, duplicated or out of order, and labels or comments cannot serve as locks, a ledger or a secret store.

## Decision

- Git is authoritative for source, manifest, docs, ADRs and decision policy. Issues, PRs, checks and releases carry work, evidence and decisions.
- Locks, leases, jobs, the budget ledger, sessions and secrets live in the state store, never in GitHub. Operational tables are derived working state and always reference a SHA, issue ID or decision revision.
- Webhooks are verified, written to an inbox, acknowledged quickly and processed asynchronously, with periodic reconciliation. Labels and comments written by the bot must not trigger loops.
- If GitHub is unreachable, Zone 1 keeps serving the verified revision; intake and publish stop or queue safely.

Rejected: GitHub labels as distributed locks; GitHub as the job queue.

## Consequences

- Every state change is stored first with its version and idempotency key, then projected to GitHub.
- The factory can lose its database without losing source or issues, but not ledger or audit history (see [ADR 0012](0012-backup-and-restore.md)).

## Acceptance test

- SISF-04 (duplicate and reordered webhooks create exactly one run, and reconciliation recovers a lost delivery): `tests/acceptance/duplicate_reordered_webhooks_test.go`, with `cmd/factoryd/wiring/control_reconcile_test.go`.
- SISF-05 (a worker crash is recovered from state and lease, with fencing): `internal/dispatcher/worker_crash_fencing_test.go`.
