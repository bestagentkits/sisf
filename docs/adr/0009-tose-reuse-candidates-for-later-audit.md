# ADR 0009: TOSE reuse candidates, for a later audit

## Status

Status: accepted. Covers brief [§11](../self-improvement-software-factory-decision-brief.en.md) inheritance.

## Context

SISF draws ideas from an internal design in the TOSE application. Reusing its code needs a provenance, license and dependency audit first, and TOSE production behavior must not change.

## Decision

This ADR imports nothing. It only records candidates for a later audit:

- Packages named `delegation`, `decision`, `scope`, `budget`, `promotion` and `upstream`.
- The contract and protocol-adapter part of the `piadapter` service, not the whole agent service.
- Excluded: the TOSE policy package, and the TOSE OAuth, wallet and Kubernetes bindings.

No TOSE code enters this repository until its provenance and license are verified and the Owner approves. Apache-2.0 applies to newly written code and documents only. Any extraction goes behind an adapter and leaves current TOSE operations unchanged.

## Consequences

- Clean-room contracts are written first; the audit may later replace or confirm them.

## Acceptance test

Deliberate non-goal: no code is imported now. A later audit ADR will define the acceptance evidence.
