# ADR 0017: Legal and data handling

## Status

Status: proposed. Closes open point 8 of brief [§15](../self-improvement-software-factory-decision-brief.en.md). Awaiting Owner decision; the recommendation below is a default, not an approved requirement.

## Context

The brief is silent on the license of agent-generated code, user data sent to model providers and data residency.

## Decision

- Generated code falls under the repository license, and the Owner is accountable for it.
- Only repository content and redacted telemetry go to providers. Personal data is denied by default.
- No data-residency guarantee is offered, and this is stated openly.

## Consequences

- Owners with residency needs must choose providers accordingly.
- The redaction and retention rules of brief §7 requirement 7 carry the technical enforcement.

## Acceptance test

Once accepted: a redaction test shows personal data is removed before provider calls, and the install flow shows the residency statement.
