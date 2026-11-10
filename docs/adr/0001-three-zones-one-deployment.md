# ADR 0001: Three zones, one deployment

## Status

Status: accepted. Covers brief [§0](../self-improvement-software-factory-decision-brief.en.md) items 2 to 5 and the §10 invariant.

## Context

SISF has an application zone (Zone 1), an agent factory (Zone 2) and a control plane (Zone 3). Splitting them into independent deployments would give each its own manifest, credentials and lifecycle, so no single place could prove what is running or roll the whole factory back. Remote cloud agents tempt a design where Zone 2 lives elsewhere.

## Decision

- All three zones belong to one factory deployment: one `factory_id`, one release manifest, one configuration lifecycle, one authority plane.
- A zone may be several processes, containers or services. It is never a separate product or a separate deployment.
- A remote agent service is an optional execution backend that Zone 2 calls. It does not replace Zone 2; the dispatcher, run state and QA orchestration stay inside the deployment.
- A platform that cannot satisfy `single_factory_deployment` is recorded as an unsupported full deployment (see [capability matrix](../capability-matrix.md)).

Rejected: three deployments linked by APIs (no shared lifecycle); Zone 2 as a hosted SaaS (a mandatory external dependency).

## Consequences

- Install, pause, update, recover and uninstall act on the whole deployment.
- Platform adapters must keep the nine runtime units under one manifest (brief §10).
- The conformance checker must be able to tell `factory_id`, `deployment_id` and runtime IDs apart.

## Acceptance test

Planned: SISF-01 (one `factory_id`, all zones in one manifest) and SISF-16 (a backend that cannot meet the contract is marked unsupported). The manifest conformance check rejects a manifest with a missing zone.
