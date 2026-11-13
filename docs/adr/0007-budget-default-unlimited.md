# ADR 0007: Budget defaults to unlimited, with metering

## Status

Status: accepted. Covers brief [§0](../self-improvement-software-factory-decision-brief.en.md) item 11 and §8.

## Context

The Owner asked for `unlimited` as the default budget. That must not be read as permission to remove other limits, nor as a promise about what a provider will charge.

## Decision

- Default `budget.mode: unlimited` means no monetary cap set by SISF. The ledger, cost display, alerts and cancel still operate.
- A fresh install shows the default and requires one Owner confirmation, together with setting `budget.alert_threshold` or explicitly declining. Until then the factory accepts intake but claims no tasks.
- Crossing the alert threshold notifies and does not stop unless the Owner chose `capped`.
- Timeouts, max attempts, concurrency, rate limits and quotas always apply. Roles and scopes never escalate on their own.
- In `capped` mode a reservation is made before a run starts. Unmeasured usage is shown as `unverified`; a provider without a hard limit is flagged.
- Usage whose provider reports tokens but no cost is recorded as reported, with no cost. When the reservation of a run with such usage is closed, the part of the reservation that the run's reported costs did not cover is charged as `unverified`; the reservation is the factory's own estimate of the run's worst case. The run's booked cost, and whether some of its usage stated none, are read from the run's rows in the append-only ledger, never from the dispatcher's record of the run: that record is the agent zone's and can be rewritten, while anything that lowers the charge in the ledger is itself booked spending. The record, and the QA judgments' own tally, can only raise the charge, for an attempt whose agent reported nothing and so left no row. A zone that may not read the ledger charges the whole reservation as `unverified` rather than trust the record. All reported costs stay `unverified`: no provider confirms them. Usage of a run that reserved nothing, as with an unlimited budget and a run estimate of 0, is not counted. `budget.get` warns when some usage had no cost reported.

## Consequences

- The state store needs a transactional ledger and reservations.
- The guarantee level is reported openly rather than overstated.

## Acceptance test

- SISF-14 (unlimited is explicit and confirmed at install, alerts fire, capped reserves and stops): `internal/budget/budget_modes_test.go`; unsupported hard caps are flagged: `internal/budget/budget_status_test.go` (`TestUnverifiedUsageAndAMissingHardCapAreShown`); usage without a reported cost is recorded as reported, charged when its run closes and warned about: `internal/budget/unpriced_usage_test.go`, `internal/budget/unpriced_usage_store_test.go`; the close reads the run's cost from the ledger, not from the agent zone's record: `internal/dispatcher/run_spend_test.go`, `cmd/supervisor/stages/run_close_reported_cost_test.go`, `pkg/state/sqlstore/run_usage_test.go`.
