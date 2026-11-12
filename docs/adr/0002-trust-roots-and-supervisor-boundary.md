# ADR 0002: Trust roots and the supervisor boundary

## Status

Status: accepted. Covers brief [§0](../self-improvement-software-factory-decision-brief.en.md) item 9 and the four conditions of §6.3.

## Context

A factory that improves its own code can weaken the machinery that judges that code. The release supervisor must be able to rescue a bad candidate without depending on anything outside the deployment, and without the candidate being able to touch it.

## Decision

- The trusted control surface is the supervisor, signing keys, policy enforcement, QA baseline and rollback rules. A candidate may not change any of them and score itself in the same pass; such a change is a governance change with human approval through a separate cycle.
- The supervisor is declared in the same `factory.yaml` and shares the `factory_id` and lifecycle, and all four conditions hold: (1) its own digest-pinned image, not built from the candidate artifact; (2) LKG, signing keys and rollback configuration on a volume or store that Zone 1 and Zone 2 processes and agent workspaces cannot write; (3) no pipeline permission to change the supervisor digest; (4) a startup self-check of its digest against the signed manifest, refusing to run on mismatch.
- If any condition is missing, self-update is disabled. A mode that cannot separate the supervisor from the candidate must not self-publish.
- The root key stays with the Owner outside the factory; the factory holds short-lived delegated keys (brief §7, requirement 5).

## Consequences

- The conformance checker verifies all four conditions and rejects violations.
- Changing the supervisor needs a human-approved update path, which is slower by design.
- The local native profile may keep the root key in the Zone 3 secret store and must display the lower assurance level.

## Acceptance test

Planned: conformance tests for each of the four conditions, SISF-10 (the supervisor is not replaced by the candidate) and the key-revocation tests in [threat model](../threat-model.md) scenarios TM-02 and TM-05.
