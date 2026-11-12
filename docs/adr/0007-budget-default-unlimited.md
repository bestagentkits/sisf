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

## Consequences

- The state store needs a transactional ledger and reservations.
- The guarantee level is reported openly rather than overstated.

## Acceptance test

Planned: SISF-14 (unlimited is explicit and confirmed at install, alerts fire, capped reserves and stops, unsupported hard caps are flagged).
