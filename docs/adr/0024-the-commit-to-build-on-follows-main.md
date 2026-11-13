# ADR 0024: The commit to build on follows main while the approved content is unchanged

## Status

Status: proposed. The Owner chose this direction on 2026-11-13: several releases of the same unit must follow one another without the Owner, with no widening of any agent permission and no skipping of the Owner's approval of foundation content. It supersedes the consequence "One self-published release per unit and baseline" and the rejected alternative "Advance the trust baseline when the factory merges its own release" of [ADR 0023](0023-every-release-ends-in-a-state-the-owner-can-act-on.md).

## Context

The policy in force (`policy/baseline`) names one commit, the revision the Owner approved. Until now every candidate was built, judged and compared on that commit. After the factory merged a release of a unit, main moved past the policy, and the next candidate of the same unit was built on a base without the merged release. The release check of [ADR 0023](0023-every-release-ends-in-a-state-the-owner-can-act-on.md) then refused it, after the run was coded and judged, until the Owner put a commit containing the release in force. Two consequences followed: a unit could be released only once per Owner approval, and a run that could never be released spent its coding and QA work first.

The policy revision is also the subject of every approval, the `PolicyRevision` of intake and the `PolicySHA` of every receipt. Moving it after each merge would void pending approvals and in-flight runs, and moving it at all is reserved for the Owner.

## Decision

- **A separate commit to build on.** The commit a candidate is built, judged and compared on is kept apart from the policy revision, in the document `control.policy/build-base` (`internal/policy/build_base.go`). The policy revision, its approval and `policy/baseline` do not change. The receipt's `BaselineSHA` names the commit to build on; its `PolicySHA` still names the policy in force. Without a record the commit to build on is the policy revision, as before.
- **Only the supervisor moves it, after its own merge, on proof.** After the supervisor merged a verified release (`internal/supervisor/release/follow_main.go`), it moves the commit to build on to the merge commit only when all of these hold:
  - the merge commit contains the current commit to build on and the candidate commit the receipt names (GitHub's three-dot comparison lists nothing), so a squash, a rebase or a diverged merge is not followed;
  - GitHub's comparison of the current commit with the merge commit changes no foundation or protected path (`stages.ProtectedPath` and `docs/adr/`);
  - the digest of the foundation files at the merge commit equals the digest the Owner approved, and they declare the same factory.
- **A change of approved content stops the factory.** When any of these fails, the supervisor records a stop with the merge, run, unit and reason and does not move the commit. While stopped, factoryd refuses every new brief with `APPROVAL_REQUIRED` before a run is claimed, and the release's issue says the factory stopped building on main. The stop ends only when the Owner approves a new policy revision: `publishPolicy` drops a record whose revision is not the one in force, and the next commit to build on is the Owner's revision.
- **Accepting a base.** The build, QA and release stages accept a base only when it is the policy revision, or a commit the supervisor followed under it whose foundation digest is recomputed equal to the approved one. A forged record, a commit never followed, or a record that cannot be read is refused.
- **Who can write it.** `control.policy` belongs to the control zone; the agent zone can read it and cannot write it (`pkg/state/sqlstore/zone_ownership.go`). The move is a service action, `policy.follow_main`, refused to any actor but `service:supervisor` and audited, allowed and denied alike.
- **GitHub that cannot answer decides nothing.** An unavailable, rate-limited, timed-out or unauthenticated GitHub leaves the record as it is and is retried with the release's finishing step.
- **A run that cannot be released stops before QA.** The build stage checks, right after the build, that the base contains the commit of the unit's LKG release (`CheckBaseHasLKG`). A candidate that fails is held with reason `base_lacks_release`, which ends the run and blocks its issue for the Owner like a superseded policy, so it is never sent to QA. The release check of [ADR 0023](0023-every-release-ends-in-a-state-the-owner-can-act-on.md) stays as a second refusal.

## Consequences

- Releases of the same unit follow one another without the Owner while main changes only paths the factory may change by itself.
- A merge that touches a foundation file, an ADR or another protected path, made by anyone, stops all new runs until the Owner approves a policy revision. Ready issues pinned to the earlier revision are then superseded at build, as after any policy change, and the Owner retries them.
- While the factory is stopped the dispatcher logs the refused brief on each tick.
- A second issue of a unit coded while the release before it is still under way is built on the commit before that release's merge. It is held before QA as `base_lacks_release`, and a plain retry codes it on the followed commit ([#71](https://github.com/bestagentkits/sisf/issues/71)).
- The record is in the control zone's namespace, which factoryd shares with the supervisor; the threat model states what a compromised factoryd could do with it ([#72](https://github.com/bestagentkits/sisf/issues/72)).
- A merge whose commit the supervisor does not know, for example after a restart between the merge and the follow, stops the factory rather than guessing.

## Alternatives considered

- **Advance the policy revision after each merge.** Rejected: it would void pending approvals and in-flight runs pinned to the revision, and it would make the factory move what only the Owner may move.
- **Let the agent zone or factoryd move the base.** Rejected: neither is the trusted writer, and the agent zone must not choose what it is judged against.
- **Follow main's head without a merge proof.** Rejected: a commit someone else pushed could carry changed foundation content into every later build.

## Acceptance test

`internal/policy/build_base_test.go` proves three releases in a row, a foundation change stopping the factory until the Owner approves, that only the supervisor moves the commit, that a tampered proof or record is refused and that an unanswering GitHub decides nothing. `tests/e2e/standalone_consecutive_releases_test.go` (build tag `e2e`, no Docker) releases two more issues of the same unit after the first with no Owner action, then commits a foundation change on main, shows that a new issue is not started, and has the Owner approve before the factory releases again.
