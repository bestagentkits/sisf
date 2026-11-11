# Policy, approvals and budget

How the Owner puts a policy in force, decides approval requests and changes the budget. Why the factory works this way: [ADR 0003](../adr/0003-policy-precedence-and-foundation-files.md) (policy precedence), [ADR 0008](../adr/0008-owner-authority-features-upstream.md) (Owner authority) and [ADR 0007](../adr/0007-budget-default-unlimited.md) (budget).

## What the policy is

The policy is the set of foundation files of the repository the factory serves, read at one commit the Owner approved (`internal/policy`):

- `AGENTS.md`, `REVIEW.md`, `DECISIONS.md`;
- `FEATURES.md` or `features.yaml`;
- `.sisf/factory.yaml`.

All must exist at that commit and agree with each other, or the answer is `POLICY_HOLD`; `DECISIONS.md`, for example, must state the schema version of its typed questions (`internal/policy/snapshot.go`). There is no starter template yet. This repository's own `AGENTS.md`, `REVIEW.md`, `DECISIONS.md` and `FEATURES.md`, with an install's `factory.yaml` as `.sisf/factory.yaml`, form a policy that was observed to be accepted; the quickstart uses them, and they are a starting point for your own. Commits made later, merged or not, are not policy until the Owner approves one of them. An agent may propose a change to these files as a pull request; it cannot put one in force.

Until the first revision is approved, nothing moves: the control zone holds intake and the agent zone claims nothing (`cmd/factoryd/wiring/control_policy.go`). Approving the first revision is therefore part of [getting a first issue through](quickstart.md#4-run-a-first-issue).

### Two files named factory.yaml

| File | Where | Read by |
|---|---|---|
| `.sisf/factory.yaml` | the served GitHub repository, at the approved commit | the control zone, as part of the policy: for example the Decision Layer's `decision.system_one` section, the egress policy and `extensions.intake.ready_label_alias` |
| `<dir>/factory.yaml` | the install directory, written by `sisf init` | `sisf up`, which builds, pins digests into it and starts the units from it; `sisf doctor` |

Nothing compares the two files; keep them consistent yourself. The end-to-end tests commit a copy of the install's file as the repository's `.sisf/factory.yaml` (`tests/e2e/lifecycle_scenario_test.go`). `sisf conformance check` with no argument checks `.sisf/factory.yaml` in the current directory; pass `<dir>/factory.yaml` to check the install's.

## Running Owner commands on the host

Putting a policy in force and retrying a Blocked issue have no `sisf` command yet. They are `factoryd` subcommands that run on the factory's host with the control zone's environment, which only someone who can read the install's secrets has (`cmd/factoryd/main.go`).

Standalone mode, with the factory running or stopped, from any directory:

```sh
D=$HOME/.sisf/factory          # your install directory
( set -a; . "$D/secrets/control/runtime.env"; set +a
  SISF_CONFIG="$D/factory.yaml" "$D/bin/factoryd" policy approve --revision <commit sha> )
```

The subshell keeps the control zone's secrets out of your shell. `$D/bin/factoryd` exists once `sisf up` has built it.

Docker mode, with the factory running (the container is `<project>-control`, where the project is `sisf-<repository name>` unless you passed `--factory-id`):

```sh
docker exec sisf-hello-control factoryd policy approve --revision <commit sha>
```

This is how the end-to-end tests run it in each mode (`tests/e2e/standalone_lifecycle_test.go`, `tests/e2e/compose_lifecycle_test.go`).

## Putting the first policy in force

Prerequisites: the factory was started once with `sisf up` (the state store exists), and the five foundation files are committed to the served repository.

1. Find the full commit SHA of that commit (40 hex characters; a short SHA is refused with `INVALID_ARGUMENT: commit "..." must be a full 40- or 64-character lowercase hash`).
2. Run `factoryd policy approve --revision <sha>` as shown above.

Expected output:

```text
policy revision <sha> is in force (content sha256:<digest>)
```

Verify: running the same command again prints the same line; the control zone takes in issues from its next pass. A failure names the cause, for example `POLICY_HOLD: AGENTS.md could not be read at <sha>` when a file is missing at that commit or GitHub cannot be reached.

Undo: a revision in force can only be replaced by another approved one (below). To start over on a test install, remove the state, which holds the policy record: `sisf down -v` while the factory runs, or delete `<dir>/data` once a Standalone factory is stopped.

## Changing the policy

Replacing the revision in force is itself a policy change and needs an Owner approval of exactly the new content (`internal/policy/baseline.go`).

1. Commit the change to the foundation files and note the new full SHA.
2. File the request: `factoryd policy request --revision <new sha>`. It prints the request id, for example `policy:3812483ea4ac:49f6b41b3c1c75d0:f214037dc809`. Asking again while it is open prints the same id.
3. Decide it on the dashboard at `/approvals/<id>` ([below](#deciding-an-approval)).
4. Put it in force: `factoryd policy approve --revision <new sha> --approval <id>`. The approval is spent.

What you see when a step is missing (observed):

| Situation | Answer |
|---|---|
| step 4 without `--approval` | `APPROVAL_REQUIRED: replacing the policy in force needs an approval of the new revision` |
| step 4 before the request is decided | `APPROVAL_REQUIRED: the approval request has not been decided` |
| step 2 for the revision already in force | `CONFLICT: this revision is already the policy in force` |

Undo: request and approve the previous revision the same way.

## Deciding an approval

Approval requests come from issues that need the Owner's scope approval, from sensitive actions such as `budget.set` and `deployments.rollback`, and from policy changes. A decision is bound to the request's nonce, subject digest and policy revision, and is used once.

- List and read them anywhere: `sisf approvals list`, `sisf approvals get <id>`, the dashboard's Approvals page (`/approvals`).
- Decide on the dashboard only: open `/approvals/<id>`, choose approve, request changes or reject, then confirm your identity at the sign-in provider when asked. The confirmation is a fresh sign-in, bound to that one request and verdict, valid for five minutes and spent by the one decision.
- `sisf approvals decide <id> ...` (and the same action over REST, MCP or WebMCP) answers `STEP_UP_REQUIRED` and points at the dashboard. With `--dry-run` it previews the decision and changes nothing (`internal/actions/service_step_up.go`).
- The confirmation needs an OpenID Connect provider. GitHub sign-in cannot prove a fresh sign-in, so an Owner who signed in with GitHub is refused with `NOT_SUPPORTED`; see the [security guide](security.md#limits-to-know).

Why this is the only path: [Signing in](operations.md#signing-in).

## Budget

The budget is confirmed once, at `sisf init` (or the first `sisf up`) with `--confirm-unlimited` or `--budget-cap`; until then the factory takes in issues and starts no work, and `sisf doctor` warns `budget  not confirmed`. A second confirmation is refused: `ALREADY_EXISTS: the budget is already confirmed; it is confirmed once` (`internal/install/lifecycle.go`).

- Read it: `sisf call budget.get`, or the dashboard's Security page (`/security`).
- Change it: the `budget.set` action is sensitive and needs step-up. On the Security page, fill in "Change the budget" and submit: this files an approval request. Decide it on its approval page, then submit the same form again with the approval id. The form takes amounts in millionths of the currency unit, while `sisf init` takes whole units.
- What each run sets aside and how rollback is funded: [State and budget](operations.md#state-and-budget).

## Secrets

The control zone's secret references and where values go: [Secrets of the control zone](operations.md#secrets-of-the-control-zone). `sisf call secrets.list_refs` lists the references, never values.
