# ADR 0021: A verified candidate lands by a supervisor merge

## Status

Status: proposed. The Owner chose this direction; the ADR records it for review before it is accepted.

## Context

A run's candidate is committed to a branch `sisf/<factory>/<run>-<attempt>`, judged by QA and released from its build. Until now nothing brought that commit to the default branch: the issue closed on a verified deployment while the repository never held the change. Nobody could see on GitHub which commit a closed issue shipped.

## Decision

- When QA passes a candidate, the supervisor opens one pull request from the candidate branch to the default branch. Its title comes from the issue; its body says `Refs #N` (never a closing keyword, so GitHub does not close the issue on merge), the Scope of Work digest, the QA receipt and its score, and ends with a marker naming the run. It is found again by its head branch, so a retry or a restart never opens a second one, but only a pull request the factory's own account opened with that marker is taken as the candidate's: any other on the branch is neither adopted, merged nor closed, and the candidate is left for the Owner. A permanent refusal to open the pull request leaves it for the Owner too. A candidate QA failed gets none.
- Only the supervisor merges, and only after the release of that exact candidate build reached Verified. Just before merging it checks again that:
  - the verified release is the build of the commit QA judged;
  - the pull request is open, targets the default branch and its head is still that commit; the merge request carries the commit as `sha`, so GitHub refuses it if the branch moved in between;
  - the policy in force is the one the run was pinned to;
  - the factory is not frozen (a frozen factory holds the issue in Verified instead);
  - the candidate changes no foundation or policy file (`AGENTS.md`, `REVIEW.md`, `DECISIONS.md`, `.sisf/**`, ADRs and the other paths of `stages.ProtectedPath`). Those are the Owner's to merge (ADR 0003).
- When a check fails or GitHub refuses the merge, the pull request is left open for the Owner with a note, the reason is recorded on the run, and the issue still closes on its verified release. The closing checkpoint says plainly that the change is not merged. A rolled-back release's pull request is never merged and gets a note.
- The order is: Verified, then the merge or its recorded reason, then the progress comment's final update, then the issue is closed. A merge GitHub could not answer keeps the issue in Verified and is tried again in a later round, including after a restart; so is one GitHub refused because the default branch was just modified, at most five times. A pull request already merged at the candidate commit counts as merged, checked before anything else, so it is merged at most once and never recorded as not merged.
- The control zone and the agents have no merge path. The merge capability (`github.Merger`) is built only from the supervisor's client.

## Consequences

- The supervisor's GitHub token needs Pull requests: write (open and merge) besides Contents: write (candidate branches; a merge writes the default branch).
- Control and the supervisor share one GitHub token in today's installs, so the boundary is in code, not in the credential: a test reads the build graph of the control and dispatcher binary (`factoryd`), the edge and `sisf` and fails if any package compiled into them names the merger. `CommitToBranch` writes only `sisf/` branches. Separate tokens per zone would make it a credential boundary.
- Runs build on the policy revision the Owner approved, not on the default branch. A merged change therefore reaches the next run's base only once the Owner approves a policy revision that contains it.
- Each candidate is judged against that approved revision, never against the default branch with the other merged candidates: after several merges the default branch is a combination no QA evaluated as a whole.
- Branch protection on the default branch may refuse the merge; the pull request is then left for the Owner, as with any other refusal.
- The default branch now moves with every verified release. Owners who want to review each change before it lands should protect the branch; the factory then leaves every pull request open.

## Acceptance test

- `cmd/supervisor/stages/candidate_landing_test.go`: merged only while the candidate is what was judged and released; a foundation file, an ADR, a changed policy, another build or a moved branch leave it for the Owner; a merge already made is not repeated.
- `tests/acceptance/candidate_pull_request_landing_test.go`: merged before the issue closes; branch moved, left for the Owner and the issue still closes; rolled back, never merged; a merge interrupted after verification is finished exactly once.
- `pkg/github/merge_capability_boundary_test.go`: only the supervisor can build a merger.
