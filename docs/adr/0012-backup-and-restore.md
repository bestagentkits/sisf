# ADR 0012: Backup and restore

## Status

Status: proposed. Closes open point 3 of brief [§15](../self-improvement-software-factory-decision-brief.en.md). Awaiting Owner decision; the recommendation below is a default, not an approved requirement.

## Context

Nothing states how `state` and `artifacts` are backed up, what recovery targets apply, or what is rebuilt from GitHub when the database is lost.

## Decision

- Snapshot `state` and `artifacts` daily and before each release.
- Targets: RPO of 24 hours or the last release, RTO of 1 hour.
- Rebuilding from GitHub restores issues and the SoW only. Loss of ledger or audit data is reported to the Owner, never hidden.
- Implementation waits for the Owner to accept this ADR.

## Consequences

- Releases get a snapshot step and a restore drill.
- The audit log may need an off-factory copy to survive loss.
- Customer data is never restored from an old snapshot by rollback (see [ADR 0005](0005-self-deploy-zone1-qa-and-rollback.md)).

## Acceptance test

Once accepted: a restore test that recovers a snapshot into a fresh deployment and reports what was lost.
