# ADR 0011: v1 scope and delivery order

## Status

Status: proposed. Closes open point 2 of brief [§15](../self-improvement-software-factory-decision-brief.en.md). Awaiting Owner decision; the recommendation below is a default, not an approved requirement.

## Context

Brief §12 proposes releasing v1 after the release phase (Compose, CLI, read-only dashboard) and moving remote MCP, WebMCP, Google SSO and serverless profiles later. Scope has grown since.

## Decision

Keep the §12 phase order. The early-v1 proposal is not adopted for now, and the surfaces and identity phase stays in scope. Revisit after the release phase, using the value metrics of brief §13.

## Consequences

- The first public release waits for the surfaces and identity phase.
- The risk of a large final phase is accepted and monitored.
- Revisiting needs a new ADR, because it changes official scope.

## Acceptance test

Deliberate non-goal until revisited: no acceptance test; the check is that the plan order matches brief §12.
