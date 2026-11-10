# ADR 0016: Open-source governance

## Status

Status: proposed. Closes open point 7 of brief [§15](../self-improvement-software-factory-decision-brief.en.md). Awaiting Owner decision; the recommendation below is a default, not an approved requirement.

## Context

The repository needs contribution rules, a version scheme and a compatibility promise for the interfaces in brief §3.

## Decision

- DCO sign-off on every commit (see [CONTRIBUTING.md](../../CONTRIBUTING.md)); no CLA.
- SemVer with 0.x versions until 1.0.
- Interfaces under `pkg/` are experimental until 1.0, and changes inside a minor version are additive only.

## Consequences

- Contributors add a `Signed-off-by` line.
- Plugin authors can rely on additive evolution but not on stability before 1.0.

## Acceptance test

Once accepted: a CI check for DCO sign-off, and a review rule that rejects non-additive interface changes within a minor version.
