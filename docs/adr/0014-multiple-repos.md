# ADR 0014: Multiple repositories

## Status

Status: proposed. Closes open point 5 of brief [§15](../self-improvement-software-factory-decision-brief.en.md). Awaiting Owner decision; the recommendation below is a default, not an approved requirement.

## Context

It is not stated whether one factory serves one repository or several, nor what would be shared.

## Decision

One factory serves one repository. More repositories mean more factory deployments, and nothing (policy, budget, secrets) is shared between them.

## Consequences

- Isolation is simple and matches the one-deployment invariant ([ADR 0001](0001-three-zones-one-deployment.md)).
- Operating many repositories costs more deployments.
- A monorepo with several apps is still one repository.

## Acceptance test

Once accepted: a conformance check rejects a manifest that binds more than one repository, and the installation mapping test covers cross-repo denial.
