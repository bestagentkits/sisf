# ADR 0018: People during an incident

## Status

Status: proposed. Closes open point 9 of brief [§15](../self-improvement-software-factory-decision-brief.en.md). Awaiting Owner decision; the recommendation below is a default, not an approved requirement.

## Context

There is no notification channel outside GitHub, no on-call owner and no procedure for when automatic rollback also fails.

## Decision

- A generic webhook or email notifier outside GitHub, with the Owner as default on-call.
- If automatic rollback fails, use the kill switch (freeze plus hold) and the manual runbook `docs/guides/incident.md`, which is written with the surfaces and identity docs.

## Consequences

- The notifier is a new outbound integration subject to egress policy.
- The runbook is a dependency; until it exists this ADR is unfulfilled.

## Acceptance test

Once accepted: a test where rollback fails still fires the notifier and leaves the factory frozen.
