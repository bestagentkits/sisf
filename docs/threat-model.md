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
| TM-09 | An approval is replayed, forged, or granted by a browser agent through an ambient Owner session | Signed single-use approvals bound to subject digest, policy revision, nonce and expiry; a single-use step-up bound to one call and accepted only from the dashboard's form post, never over REST or WebMCP; no ambient session impersonation | §5.4, §7 |
| TM-10 | Audit records are edited or deleted to hide an action | Append-only hash-chained log or off-factory copy; no role can edit a record | §7 req. 6 |
| TM-11 | A malicious PR from a fork runs with secrets or write access | Fork PRs are untrusted: sandbox without secrets or repo write, Maintainer acceptance before entering the pipeline; private repos accept collaborators only | §7 req. 4 |
| TM-12 | A loop or provider quirk causes runaway spend | Timeouts, max attempts, concurrency and rate limits always on; alerts; reservation in capped mode | §8; SISF-14 |
| TM-13 | Secrets or personal data reach logs, transcripts, issues, PRs or providers | Redaction before write; retention per data kind; policy on what providers may receive | §7 req. 7 |
| TM-14 | The compromised or failed factory cannot be stopped | Emergency stop that does not depend on the possibly compromised part: stop claims, cancel runs, revoke per-run secrets, freeze publishing | §7 req. 8 |
| TM-15 | Rollback overwrites customer data, or a release is published when metrics or rollback are unavailable | Rollback replaces compatible code and config only; hold when metrics or rollback are missing | §6.2; SISF-09, SISF-20 |

### Candidate branches and CI (TM-01, TM-08)

The supervisor commits each agent's patch with its GitHub token to a branch `sisf/<factory>/<run>-<attempt>` of the factory's repository, before QA has judged it. Required behavior:

- Before anything is committed or pushed, the builder refuses a patch that touches `.github/**` (workflows, composite actions and every other file there), `CODEOWNERS` at any location GitHub reads it from, the factory's policy files (`.sisf/**`, `AGENTS.md`, `REVIEW.md`, `DECISIONS.md`, `FEATURES.md`, `features.yaml`) or a schema migration. The refusal is a failed build of that attempt, so the run's next coding attempt follows. The QA permissions and migrations gates use the same path rule (`stages.ProtectedPath` in `cmd/supervisor/stages`).
- A path no commit can hold (a `.git` segment in any letter case, `.` or `..`, NUL, over 4096 bytes) is refused the same way, before any write.
- What the patch's content makes fail is charged to its attempt with a typed reason: a base file it edits that is a directory or submodule (`not_a_regular_file`) or over 1 MB (`base_file_too_large`), a tree GitHub refuses (`path_rejected`, `commit_too_large`), a build command that fails, times out or reports no image (`build_failed`, `build_timeout`, `no_image_reported`). Every other failure (network, 5xx, 401/403, 404 on the base or branch, rate limits, Docker, a sandbox that cannot start) is an outage: retried with backoff without spending an attempt and, after `--build-outage-limit` outages in a row (default 5), held. A held build is not tried again; it is listed in the supervisor's `/status` (`held_builds`), the Owner is told on the issue, and `factoryd issue retry` by the Owner resumes it. The decision for each GitHub status is the table `statusOutcomes` in `cmd/supervisor/stages`.
- The supervisor's token should not carry the `workflow` scope; `sisf doctor` warns when a classic token reports it.
- **Operator requirement:** CI must not run secret-bearing jobs on pushes to `sisf/**` branches. A candidate branch holds code no reviewer has seen yet; anything CI already runs on a push (build scripts, test fixtures, `Makefile` targets) is under the agent's control there. Run secret-free checks only, or restrict secret-bearing workflows to protected branches and reviewed pull requests.

### Merging a verified candidate (TM-02, TM-08)

The supervisor merges a candidate's pull request into the default branch with its GitHub token ([ADR 0021](adr/0021-candidate-lands-by-supervisor-merge.md)). This is the factory's only write to the default branch. Required behavior:

- The merge happens only after the release of that exact candidate build reached Verified, and only while the verified release is the build of the commit QA judged, the pull request targets the default branch, and its head is still that commit. The merge request carries the commit as `sha`, so GitHub refuses it if the branch moved (`cmd/supervisor/stages/candidate_landing.go`).
- A candidate that changes a foundation or policy file (`stages.ProtectedPath`, ADRs included) is never merged by the factory; the Owner decides. The builder refuses `ProtectedPath` patches and release governance refuses the wider trusted control surface before release; this is a third check at merge time.
- No merge while the policy in force differs from the policy the run was pinned to, or while the factory is frozen. A rolled-back release is never merged.
- Only the supervisor can merge. `github.Merger` is built from the supervisor's client; `*github.RESTClient` and the `github.Client` interface expose no merge, and a test reads the build graph of the `factoryd` (control and dispatcher), edge and `sisf` binaries and fails if any package compiled into them names the merger under any import name (`pkg/github/merge_capability_boundary_test.go`). `CommitToBranch`, which control also holds, writes only the factory's `sisf/` branches, so it cannot move the default branch.
- Only the pull request the factory's own account opened for the run, carrying the run's marker, is merged. The author is told by login: an install may name the App's numeric ID, which another user's ID can equal, so an ID never replaces the login, and an install that names an ID but no login (`extensions.github_app_login`) merges nothing and leaves every candidate for the Owner; `sisf doctor` warns of it. A pull request someone else opened on a candidate branch is never adopted, merged or closed; the candidate is left for the Owner, and the reason names an author who is not the configured account (`cmd/supervisor/stages/candidate_pull_request_open.go`).
- The foundation and governance checks see every path of the patch, from the builder, besides GitHub's comparison. A patch changes at most 64 files and is committed on the approved baseline, so the comparison stays far below GitHub's 300-file listing limit; should a comparison ever reach it, the candidate is marked as listed in part and release governance and the merge leave it for the Owner.
- Text the supervisor and control put on GitHub never quotes the agent zone's records unchecked: the Scope of Work digest is quoted only when well formed, and only the factory's own failure codes are shown, at most ten.
- **Limit:** control and the supervisor hold the same GitHub token in today's installs, so the boundary is in code, not in the credential. A compromised control process could merge with that token. Separate per-zone tokens would make it a credential boundary.
- **Operator requirement:** protect the default branch if every change must be reviewed by a person before it lands; the factory then leaves each pull request open for the Owner.

### Agent process environment (TM-01, TM-13)

A coding or QA agent never inherits the environment of the process that starts it: it gets a PATH, a private HOME and TMPDIR, its own provider's credential variables and the names the operator allows (`--agent-env`, `--qa-agent-env`); names starting with `SISF_`, `EDGE_` or `DOCKER_` and the state store's DSN variable are always refused (`pkg/agent/agentenv`). Because a child of the same OS user can still read its parent's `/proc/<pid>/environ`, the dispatcher starts a real coding agent only when it runs `--sandboxed` with an isolating execution backend, when `--agent-binary` names a sandboxing launcher, or when the manifest states `trusted_code_only`, which `sisf doctor` reports as a warning.

Under `trusted_code_only` only, the operator may let an agent use an existing home of their choosing, such as their own, where a subscription login of `claude` or `codex` is kept: the dispatcher's `--agent-home` and, with `--exec local`, the supervisor's `--qa-home`. Both are refused with `--sandboxed`, a launcher (`--agent-binary`) or `--exec docker`, whose sandbox owns the agent's home, and when the directory is, holds or lies inside the factory's secrets, state or trust directories after symbolic links are resolved. TMPDIR stays private. The overlap check ignores letter case on macOS and Windows. In the default layout the install directory (`~/.sisf/factory`) lies inside the operator's home, so that home is refused; install the factory outside it (`sisf init --dir`) to use it.

Medium and Low scenarios (for example report-endpoint abuse, label spoofing, stale leases) are tracked in the owning tests and are not given IDs here.

## Coverage rule

Each `TM-nn` above must be referenced by at least one test or by an explicit deliberate non-goal in an ADR. The coverage test lives under `tests/security/` when it is built; until then this table is the contract.
