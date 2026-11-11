# Why is my issue stuck?

How to find out why an issue does not move, and what moves it again. The lifecycle states and their transitions are the state machine of the decision brief, section 5.2 ([English](../self-improvement-software-factory-decision-brief.en.md)); the code is `pkg/core/states.go` and `pkg/core/statemachine`, and a docs test keeps the brief's diagram equal to the code.

## Start here

```sh
sisf login                              # once; see the quickstart
sisf status                             # issues per state, stopped issues, open approvals, GitHub sync
sisf runs explain owner/repo#12         # why this issue's run stands where it does
sisf runs inspect owner/repo#12         # the full record: state, attempt, run, pull request
```

The same answers are on the dashboard: the Workboard (`/workboard`), Runs (`/runs`), Approvals (`/approvals`) and the list of stopped issues at `/blocked`.

`sisf runs explain` answers with a `cause`, a `reason`, the `evidence` it rests on and `next_actions` (`internal/actions/runactions/run_explain.go`):

| `cause` | State | What it means | What moves it |
|---|---|---|---|
| `awaiting_approval` | ApprovalPending | the issue waits for the Owner to approve its scope | decide the request on the dashboard ([approvals](policy-and-approvals.md#deciding-an-approval)); `next_actions` names it |
| `awaiting_information` | Clarifying | the factory asked the reporter questions on GitHub (label `sisf:needs-info`) | the reporter answers on the issue; a person removing `sisf:needs-info` also resumes it |
| `attempts_exhausted` | QAEvaluation (a QA hold), or the state where the run gave up | QA held the candidate, or the run used every attempt it was allowed; the control zone then blocks the issue in its next pass | read the evidence; a QA hold has no step out in this release and an Owner retry of it is refused |
| `blocked` | Blocked | the issue stopped on an unsafe or failed step; when the block has a recorded cause, the `reason` says it and the evidence reads `blocked with cause <cause>` ([causes](#causes-of-a-block)) | fix the cause, then [retry it](#retrying-a-blocked-issue) |
| `release_refused` | Blocked | the supervisor refused the release before anything was deployed; the `reason` names why from the refusal's code (`EXPIRED`: the QA receipt or a signing key expired; `APPROVAL_REQUIRED`: the change touches the trusted control surface or builds a unit outside `release.units`; any other: the candidate did not verify) | fix the cause if there is one, then [retry it](#retrying-a-blocked-issue); the retry starts a new run |
| `release_held` | ReleaseCandidate | the supervisor held the release before anything was deployed, for example for too little traffic with no synthetic suite | remove the cause, then the Owner retries it: [a held release](operations.md#a-held-release) |
| `rolled_back` | RolledBack | the release was rolled back; the issue moves to Blocked once the recovery report is recorded | read the evidence; see the [incident runbook](incident.md) |
| `finished` | Verified or Closed | the deployed version met its definition of done | nothing; a Verified issue closes once its candidate has landed or was left for the Owner |
| `in_progress` | any other | the run is not stopped | wait; `sisf runs inspect` shows the current step |

## Holds that stop the whole factory

When no issue moves at all, check these first.

| Symptom | Check | Cause and fix |
|---|---|---|
| Nothing is taken in; `runs explain` on a new issue answers `NOT_FOUND` | the issue's labels | Intake takes only issues labelled `sisf:ready` (or the alias in `extensions.intake.ready_label_alias` of the policy). |
| Nothing is taken in although the label is there | `factoryd policy approve` was never run | No policy is in force, so intake holds. [Put the first policy in force](policy-and-approvals.md#putting-the-first-policy-in-force). |
| Issues reach Ready and stay there | `sisf doctor` shows `WARN budget  not confirmed` | The budget was never confirmed; the dispatcher claims nothing. Confirm it once with `sisf up --confirm-unlimited --no-alert` (or `--alert-threshold`, or `--budget-cap`). |
| Nothing is claimed, nothing publishes | `sisf emergency-stop --status` (Standalone) | An emergency stop is in force. Lift it once the cause is fixed: [incident runbook](incident.md#1-stop-the-damage). |
| Issues wait in Verified | `sisf runs explain` | The candidate's pull request is due to merge but GitHub cannot be reached or a stop is in force; it is retried. See [candidate pull requests](operations.md#progress-on-the-issue-and-the-candidates-pull-request). |
| A change on GitHub is not seen | `sisf status`: `github.backlog`, `github.retrying`, `github.skipped`, `github.token_access` | Reconciliation is working off a backlog, retrying a change, has given one up, or (development install) the token lost write access. See [Reconciliation with GitHub](operations.md#reconciliation-with-github). |
| Candidate builds never run in Standalone | `sisf up` reported `PERMISSION_DENIED` from the supervisor | With `--exec local` candidate builds are held by design. Give the install a [Docker build sandbox](install.md#building-in-a-docker-sandbox). |
| A build stopped retrying after outages | the supervisor's status, below | The supervisor holds a build after `--build-outage-limit` outages in a row (default 5); the Owner is told on the issue and reminded once a day, at most three times, and the build waits for the Owner's retry of its issue (`cmd/supervisor/stages/build_retry.go`). |

The supervisor's status lists held builds (`held_builds`), its self-check and the last known good release, as JSON:

```sh
"$D/bin/sisf-supervisor" status                     # Standalone; $D is the install directory
docker exec sisf-hello-supervisor supervisor status --admin=unix:/run/sisf/admin/supervisor.sock   # Docker
```

The Docker form is the container's own health check (`templates/compose/compose.yaml`).

## Causes of a block

When the control zone or the supervisor blocks an issue, it records the cause with the step, and the issue's progress comment and `sisf runs explain` show a summary of it and the way out (`cmd/supervisor/handoff/run_stop.go`). The causes are listed in `pkg/core/statemachine/retry_guard.go`:

| Cause | What happened | What moves it |
|---|---|---|
| `run_exhausted` | the run used every coding attempt it was allowed without a candidate; the summary names the code its last attempt failed with | remove the cause, then retry |
| `policy_superseded` | the Owner put another policy in force while the run was under way, so its patch is not built, judged or released | retry; the scope is judged again under the policy in force, so an issue that needs approval waits for it again |
| `release_refused` | the supervisor refused the release before anything was deployed | fix the cause if there is one, then retry |
| `rolled_back` | the release was judged unhealthy and rolled back to the last known good release | read the recovery report, remove the cause, then retry |
| `release_interrupted` | a release whose rollout had started stopped being watched before it was judged, for example because the supervisor stopped; the last known good release was restored, and nothing is known about the release's health | read the recovery report, then retry |
| `release_unjudged` | a release could not be observed, measured or synthetically checked while it served traffic; the last known good release was restored | read the recovery report, remove the cause, then retry |
| `approval_denied`, `approval_expired` | the Owner rejected the scope or asked for changes, or the approval lapsed | retry; the scope needs a new approval, or the policy's delegation must permit it |

`sisf runs explain` answers `release_refused` as its own cause; for every other cause it answers `blocked`, and the cause is in its evidence. Every retry of a block starts a new run, which spends agent budget. A refused candidate itself is never released.

## An issue that stays in ReleaseCandidate

The factory publishes by itself only the units that `release.units` in the install's `<dir>/factory.yaml` names, which is the manifest the supervisor reads (`cmd/supervisor/self_publishable_units.go`). The manifest `sisf init` writes names `app` alone (`internal/install/manifest_build.go`). A candidate that builds a unit outside `release.units`, such as a change to the factory's own control unit, is refused with `APPROVAL_REQUIRED` and its issue is blocked; `sisf runs explain` answers `release_refused` (`tests/acceptance/release_units_owner_choice_test.go`). Such a change is published only in a governance update a person makes.

Only a held release waits in ReleaseCandidate: `sisf runs explain` answers `release_held`, and the way out is in [a held release](operations.md#a-held-release). Only a refusal that judged the candidate or the request blocks the issue; a check that failed for any other reason judged nothing, and the release is tried again on the supervisor's next pass while the issue stays in ReleaseCandidate ([ADR 0023](../adr/0023-every-release-ends-in-a-state-the-owner-can-act-on.md), proposed).

## Retrying a Blocked issue

A Blocked issue moves again only through the Owner's retry, which sends it back to Ready (`cmd/factoryd/roles.go`). A block over a refused, rolled-back, interrupted or unjudged release, or over a run that used its attempts, is retried on the Owner's authorization alone; a block over an approval that was not given, or over a superseded policy, needs the scope approved or permitted again (`pkg/core/statemachine/retry_guard.go`). Run it on the host with the control zone's environment, as for the policy commands ([how, per mode](policy-and-approvals.md#running-owner-commands-on-the-host)):

```sh
factoryd issue retry --issue owner/repo#12
```

Expected output: `owner/repo#12 is Ready`. An issue the factory never took in answers `NOT_FOUND: issue owner/repo#12 has no record` (observed). A second retry of the same block answers `INVALID_TRANSITION`, and a retry by anyone but the Owner is refused. Undo: none; the retried issue is claimed again like any Ready issue.

## Candidate pull requests and merges

Where a candidate's pull request stands (`open`, `pending`, `merged`, `left_for_owner`, `rolled_back` or `refused`), when the supervisor merges it and when it leaves it for you: [Progress on the issue and the candidate's pull request](operations.md#progress-on-the-issue-and-the-candidates-pull-request). `sisf runs explain` gives the reason a pull request was left open.
