# ADR 0027: The providers' own report bounds what the agents report spent

## Status

Status: proposed. It narrows the limit stated in [ADR 0007](0007-budget-default-unlimited.md), under which a compromised agent zone can under-report a run's cost down to one micro.

## Context

The agent zone meters its own runs: the dispatcher books what its agents report, and the store can bound and date those rows but cannot tell an honest small cost from a false one. A capped budget is therefore only as hard as the agent zone is honest. Anthropic and OpenAI both report what an organization was charged, through admin APIs an agent never needs: Anthropic's Usage and Cost Analytics API (`GET /v1/organizations/analytics/cost`, beta header `analytics-usage-cost-2026-08-24`) and OpenAI's organization Costs API (`GET /v1/organization/costs`). Their figures lag: Anthropic refreshes about every 4 hours and revises figures until about a week after the month ends.

## Decision

- **An Owner setting, failing closed.** `budget.set` takes `provider_cost_sources` (`anthropic`, `openai`) for a capped budget with a period of a day, a week or a month (`internal/budget/settings.go`). A budget without a period is refused: the providers report a bounded past, not everything ever spent. Removing a source is warned about when the change is approved.
- **Read by the control zone only.** The control zone reads each named provider's spend since the start of the period (`internal/budget/provider_spend_refresh.go`, `internal/budget/providercost/`), every 15 minutes (`ProviderSpendEvery`), or a minute after a failed read (`ProviderSpendRetry`), and stores it at `install/provider_spend`. The control zone writes that namespace, and the agent zone may only read it, in the store and in the PostgreSQL row policies generated from the same rule (`pkg/state/sqlstore/zone_ownership.go`); no migration is needed. The admin keys are `SISF_ANTHROPIC_ADMIN_KEY` and `SISF_OPENAI_ADMIN_KEY`, set only in the control zone's environment; no agent environment accepts a `SISF_` variable (`pkg/agent/agentenv`).
- **The factory's own workspace or project.** Each key comes with the one workspace (`SISF_ANTHROPIC_COST_WORKSPACE`) or project (`SISF_OPENAI_COST_PROJECT`) the factory's runs are charged to. A key without it is refused at start, so other work in the organization is never counted as the factory's.
- **A floor, not a ledger entry.** The gate counts a period as having spent the larger of what the agents reported and what the providers report (`internal/budget/provider_spend.go`); reservations are added on top. The providers' figure is kept apart: it is not written to the ledger and not shown as verified usage. `budget.get` shows it as `provider_spent_micros` and warns when it exceeds what the agents reported.
- **Staleness holds new capped runs, never a rollback.** A named provider whose figure is missing, damaged, or read more than 2 hours ago (`ProviderSpendMaxAge`, the age of the control zone's read, not of the provider's data) refuses a capped claim with `BUDGET_EXCEEDED`, which the dispatcher treats as a hold on every claim. A missing credential leaves the figure to go stale, so a configuration gap holds rather than drops the check. `ReserveRollback` is never held by the provider figure. A fresh figure of the previous period, as at the start of a day, neither counts nor holds: the provider has not reported the new period yet.
- **Exact money.** Amounts are parsed as decimals, never through a float, and rounded up to the micro; a negative amount, such as a credit, counts as 0. Only USD is accepted; another currency fails the read. Redirects are not followed, so an admin key is sent only to the provider.

## Consequences

- An agent zone that under-reports is caught only once the provider reports the spending, hours later, and only up to the cap: runs claimed before then were admitted on the agents' word. The guarantee is a bound on a period's spending after the provider's lag, not on any one run.
- The gate runs in the agent zone's dispatcher. The floor corrects what the gate admits when the reported costs are wrong, through a metering bug or a run's false report; an agent zone taken over as a whole can skip the gate, as before, and the providers' figure in `budget.get`, read by the control zone, is then how the Owner sees what was spent.
- OAuth or subscription sign-ins have no billing figure and cannot use this check.
- Both adapters are written from the providers' published API references and tested against fakes only; they are not yet proven against the live APIs. OpenAI's reference does not state the unit of `amount.value`; dollars are assumed, as its examples show.
- A provider outage longer than 2 hours holds new capped runs until it ends.

## Acceptance test

- The providers' figure bounds what the agents report, lagging figures do not lower it, and a missing, stale, future-dated or partial figure holds runs but not a rollback: `internal/budget/provider_spend_test.go`.
- Only the control zone writes the figure, through the store and, on PostgreSQL, over plain SQL as the agent role; reads on schedule; a failed or unconfigured provider stores nothing: `internal/budget/provider_spend_store_test.go`.
- Exact amounts, pagination, expired cursors, refused currencies and keys, and no redirect: `internal/budget/providercost/provider_cost_sources_test.go`.
- A control pass reads the spend, a key needs its scope, and no admin key reaches an agent: `cmd/factoryd/wiring/control_provider_spend_test.go`, `cmd/factoryd/wiring/provider_cost_env_test.go`.
