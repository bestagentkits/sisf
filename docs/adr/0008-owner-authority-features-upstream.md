# ADR 0008: Owner authority over features, policy and upstream

## Status

Status: accepted. Covers brief [§0](../self-improvement-software-factory-decision-brief.en.md) items 1 and 12.

## Context

SISF is open source and may serve many deployments, each with an Owner. Agents can improve what exists, but deciding what the product is, and what leaves the deployment, is a human call.

## Decision

- The Owner alone approves new features, wider scope or permissions, significant policy changes and any contribution sent upstream. A reporter does not become an Owner.
- Within delegated scope an agent may improve existing functionality on its own.
- Improvements belong to one deployment unless the Owner chooses to share them. Nothing flows upstream automatically.
- Automatic upstream contribution is a non-goal for the first release.

## Consequences

- A new-feature issue goes to the approval inbox and waits for an Owner decision on every surface.
- A final human verification step cannot be bypassed by an agent.

## Acceptance test

Planned: SISF-03 (an unapproved feature enters the inbox and expands no permission) and SISF-15 (a local improvement in one deployment changes neither another deployment nor upstream).
