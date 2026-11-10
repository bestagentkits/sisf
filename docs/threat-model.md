# Threat model

Scope: the SISF design in the [decision brief](self-improvement-software-factory-decision-brief.en.md). This is a design-time model. Mitigations below are required behavior, not a statement that they are built. Severity uses Low, Medium and High; every High scenario has a stable ID (`TM-nn`) that a test must reference. IDs are never reused or renumbered.

## Assets

| Asset | Why it matters |
|---|---|
| Root key and delegated signing keys | Forging approvals, receipts or artifacts |
| GitHub App tokens, Owner sessions and personal access tokens | Write access to the repository |
| Provider and per-run secrets | Spend and data exposure |
| Release supervisor, LKG, QA baseline, policy files | The trusted control surface (brief §6.3) |
| State store, ledger and audit log | Truth about what happened and what was spent |
| Customer data in Zone 1 | Must survive rollback intact |
| Source repository and release artifacts | Integrity of what runs |

## Trust boundaries

1. Outside world to Zone 1 (public traffic, report endpoint).
2. Outside world to Zone 3 (webhook route, login, MCP).
3. Zone 1, Zone 2 and Zone 3 from each other (separate credentials, network segment and store role, brief §7 requirement 2).
4. Runner and QA runner to everything else (sandbox network, egress proxy only).
5. Candidate code to the supervisor and its volume (brief §6.3).
6. Factory to external services (GitHub, identity providers, model providers).
7. Human or agent client to the action service (one policy layer for every surface).

Untrusted by default: issue and comment text, telemetry, feedback, outside PRs, logs, model output, tool descriptions.

## High-severity abuse scenarios

| ID | Scenario | Required control | Brief ref |
|---|---|---|---|
| TM-01 | Prompt injection in an issue, comment, log or PR makes an agent leak a token, widen its permissions or edit policy | Operational data is data only; per-run secret broker; deny-by-default egress; policy edits need Owner approval | §7 req. 1, 4; SISF-19 |
| TM-02 | A candidate release modifies the supervisor, signing keys, policy enforcement, QA baseline or rollback rules | Four supervisor-boundary conditions; governance change needs human approval; self-update disabled if any condition is missing | §6.3; SISF-10 |
| TM-03 | A manipulated agent exfiltrates data through a network call | Egress allowlist enforced by the execution backend; backends that cannot enforce it never run untrusted code automatically | §3.2, §7 req. 1 |
| TM-04 | The QA agent scores its own patch, or a high score hides a hard-gate failure, or a receipt from another commit is reused | Independent identity, workspace and model route; receipt bound to digest, suite and baseline; hard gates override score | §6.1; SISF-06, SISF-07 |
| TM-05 | A delegated signing key or the Zone 3 store is compromised | Root key held outside the factory; short-lived delegated keys; revocation without replacing the root; keys readable only by supervisor and approval service | §7 req. 5 |
| TM-06 | A forged, replayed or reordered webhook triggers work or duplicate spending | HMAC verification, delivery-ID dedup, inbox transaction, fencing and idempotency keys, reconciliation | §5.2; SISF-04 |
| TM-07 | Zone 1 is compromised and reaches Zone 3 or Zone 2 secrets | Per-zone credentials, network segment and store role; no Zone 1 write path into Zone 3; conformance check | §7 req. 2 |
| TM-08 | A poisoned dependency, base image, CI change or unsigned artifact reaches release | Supply-chain hard gate; pinned versions; vulnerability scan; Owner approval for new dependencies; SBOM and provenance; supervisor rejects unverifiable signatures | §7 req. 3 |
| TM-09 | An approval is replayed, forged, or granted by a browser agent through an ambient Owner session | Signed single-use approvals bound to subject digest, policy revision, nonce and expiry; step-up; no ambient session impersonation | §5.4, §7 |
| TM-10 | Audit records are edited or deleted to hide an action | Append-only hash-chained log or off-factory copy; no role can edit a record | §7 req. 6 |
| TM-11 | A malicious PR from a fork runs with secrets or write access | Fork PRs are untrusted: sandbox without secrets or repo write, Maintainer acceptance before entering the pipeline; private repos accept collaborators only | §7 req. 4 |
| TM-12 | A loop or provider quirk causes runaway spend | Timeouts, max attempts, concurrency and rate limits always on; alerts; reservation in capped mode | §8; SISF-14 |
| TM-13 | Secrets or personal data reach logs, transcripts, issues, PRs or providers | Redaction before write; retention per data kind; policy on what providers may receive | §7 req. 7 |
| TM-14 | The compromised or failed factory cannot be stopped | Emergency stop that does not depend on the possibly compromised part: stop claims, cancel runs, revoke per-run secrets, freeze publishing | §7 req. 8 |
| TM-15 | Rollback overwrites customer data, or a release is published when metrics or rollback are unavailable | Rollback replaces compatible code and config only; hold when metrics or rollback are missing | §6.2; SISF-09, SISF-20 |

Medium and Low scenarios (for example report-endpoint abuse, label spoofing, stale leases) are tracked in the owning tests and are not given IDs here.

## Coverage rule

Each `TM-nn` above must be referenced by at least one test or by an explicit deliberate non-goal in an ADR. The coverage test lives under `tests/security/` when it is built; until then this table is the contract.
