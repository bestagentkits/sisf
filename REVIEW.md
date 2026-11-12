# REVIEW.md

QA and release criteria for SISF changes. Changes to this file need Owner approval; an agent must not lower a threshold on its own. Thresholds are starting values to calibrate (brief §6.1, §15), not proven numbers.

## Rubric (starting weights, total 100)

| Category | Weight |
|---|---|
| Correctness and regression | 30 |
| Security and auth | 25 |
| Contract and data safety | 20 |
| UX, performance, output | 15 |
| Observability and rollback | 10 |

Default pass: score of at least 85 with no hard-gate failure. A high score never overrides a failed hard gate.

## Hard gates

Tests and contracts; permissions; migrations; secrets; confidence in the observed build; independent evaluator; resource and budget policy; rollback readiness. A change to dependencies, lockfiles, base images or CI configuration is a hard gate of its own (brief §7 requirement 3).

## Required evidence

- Tests that were red before the behavior, plus negative tests for security and spending rules.
- For a release candidate: a QA receipt bound to the candidate digest, suite digest and baseline SHA, with the model route of both coding and QA agents.
- A rollback preflight and an observed revision after deploy.
- Honest labeling of anything experimental or unverified.

## Reviewer checklist

- Does the change keep the three zones in one deployment?
- Does it widen permissions, egress, budget or scope without Owner approval?
- Does it change the supervisor, keys, policy enforcement, QA baseline or rollback rules? If so it is a governance change, not a normal PR.
- Is every new claim in docs backed by code or a test?
- Is every `pkg/` change additive?
