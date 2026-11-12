# ADR 0003: Policy precedence and foundation files

## Status

Status: accepted. Covers brief [§0](../self-improvement-software-factory-decision-brief.en.md) item 7 and §4.1.

## Context

Agents read many instructions: code, signed policy, repository documents, issues, comments and logs. Without a fixed order a model could pick the most convenient rule, or edit a rule so that its own work passes.

## Decision

- Foundation files are `AGENTS.md`, `REVIEW.md`, `DECISIONS.md`, `FEATURES.md` (or `features.yaml`), `docs/adr/` and `.sisf/factory.yaml`.
- Precedence, highest first: safety invariants in code and verified delegated authority; Owner-approved policy and config at a protected SHA; `DECISIONS.md`, `REVIEW.md`, `FEATURES.md`, ADRs; execution guidance in `AGENTS.md`; issues, comments and logs.
- A conflict, or a SHA that is not approved, results in hold. The model never chooses between conflicting rules.
- Markdown guides a model but never replaces a deterministic check; a clause that must be enforced needs a schema or a test. Each decision records the policy, review, decision and feature-catalog SHAs plus the model route.
- An agent may propose a PR against these files but cannot lower a threshold or merge its own policy change.

## Consequences

- An unapproved edit to `DECISIONS.md` leaves the old baseline in force.
- Each root file states its owner and edit rules; see [AGENTS.md](../../AGENTS.md), [REVIEW.md](../../REVIEW.md), [DECISIONS.md](../../DECISIONS.md) and [FEATURES.md](../../FEATURES.md).

## Acceptance test

- SISF-11 (an unapproved agent PR to `DECISIONS.md` does not change the policy baseline): `internal/policy/unapproved_policy_change_ignored_test.go`.
