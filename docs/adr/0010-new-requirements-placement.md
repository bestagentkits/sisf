# ADR 0010: Placement of the new requirements

## Status

Status: proposed. Closes open point 1 of brief [§15](../self-improvement-software-factory-decision-brief.en.md). Awaiting Owner decision; the recommendation below is a default, not an approved requirement.

## Context

Brief §3.5, the experience requirements in §9, the additional security requirements in §7 and the deployment design in §10 were added after the phase scope was written. They belong to no phase and have no SISF-xx test.

## Decision

Place each requirement in a phase and give it a named test:
- §3.5 model routing: lands with the agent runs in the agent-and-release phase.
- §9 agent, user and developer experience: the manifest JSON Schema and conformance kit with the foundation; `sisf doctor` and the local simulation mode with the local factory; typed errors, `runs.explain` and generated `llms.txt` with the action service; the approval inbox, progress comment and setup wizard with the full dashboard.
- §7 additional security requirements 1 to 8: each has its own acceptance test, and requirements 1 to 3 must exist before untrusted code first runs. Requirement 6 (audit log) lands early because approvals already audit.
- §10 runtime units and operating modes: the install and unit layout in the local-factory phase, release and self-update in the release phase.

The implementation plan holds the exact package mapping; this ADR holds the decision.

## Consequences

- The brief's "no home in the SoW" gap closes once accepted.
- A requirement without a test stays a stated non-goal until one exists.

## Acceptance test

Once accepted: every requirement above maps to a named test, and `docs/threat-model.md` is covered by an automated check.
