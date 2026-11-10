# ADR 0013: Schema migration on self-update

## Status

Status: proposed. Closes open point 4 of brief [§15](../self-improvement-software-factory-decision-brief.en.md). Awaiting Owner decision; the recommendation below is a default, not an approved requirement.

## Context

Code rollback has rules. Rollback after a database schema change does not.

## Decision

- Only expand/contract migrations, compatible across the current and previous version (N and N-1).
- An incompatible migration needs Owner approval and a pre-migration snapshot, and the migration hard gate blocks self-publish of it.
- Migration state carries a `backward_compatible` flag.

## Consequences

- Some changes take two releases.
- A rollback never needs a data restore for compatible migrations.

## Acceptance test

Once accepted: a migration test rejects an incompatible migration from self-publish, and a compatible one rolls back with data intact.
