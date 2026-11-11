# Troubleshooting

Real messages of `sisf`, `sisf doctor` and `factoryd`, what causes them and what fixes them. Messages marked (observed) were produced by running the command; the others are quoted from the code that prints them. For an issue that does not move, start with [Why is my issue stuck?](stuck-issues.md); for an outage, the [incident runbook](incident.md).

`sisf doctor` prints one line per check: `OK`, `WARN` or `FAIL`, the check's name and the detail. After a report with failures it also prints `sisf: INTERNAL: the install has failing checks` and exits 1; that line only repeats the summary and is not a fault in `sisf`. Read the `FAIL` lines above it.

## Installing and starting

| Message | Cause | Fix |
|---|---|---|
| `sisf: INVALID_ARGUMENT: the value of SISF_STATE_DSN has a character an env file cannot carry safely` | the install directory's path contains a space, which cannot be written to an env file (`init` names the derived variable, not the path) | use a path without spaces; delete the empty directory the failed `init` left |
| `sisf: ALREADY_EXISTS: an install already exists in <dir>` (observed) | `init` on an existing install; nothing was changed | `sisf up --dir <dir>` starts it; to start over, `sisf down -v` and delete `<dir>` |
| `<path> is not a source checkout (no go.mod); pass --source` | `sisf up` builds from the current directory, which is not a checkout of this repository (for example under a service manager) | run it from the checkout, or pass `--source <checkout>` ([service units](install.md#running-standalone-as-a-service)) |
| `this install is already running (process N)` | a Standalone `sisf up` for this directory is running | use that one, or `sisf stop` first |
| `this install runs in <mode> mode; the mode is fixed when it is created`, `the simulation profile is fixed when the install is created` | `up` was given a `--mode` or `--profile` other than the install's | drop the flag; make a new install for another mode |
| `the budget is already confirmed; it is confirmed once` | a second budget confirmation at `up` | change it through the dashboard ([budget](policy-and-approvals.md#budget)) |
| `init` prints `Type "unlimited" to confirm it:` and then `Not confirmed. The factory will only take in issues.` | no budget flag was given; the prompt also appears when standard input is not a terminal, and before other errors | pass `--confirm-unlimited --no-alert` (or `--alert-threshold`), or `--budget-cap` |
| `--dev and --profile sim are different profiles; choose one`, `--budget-cap and --confirm-unlimited are two different budgets; choose one` | contradictory flags | follow the message |
| the supervisor refuses with `PERMISSION_DENIED`, reported by `sisf up` | a Standalone install outside the simulation with `--exec local`: candidate builds are held by design | add a [Docker build sandbox](install.md#building-in-a-docker-sandbox) |

## Stopping and removing

| Message | Cause | Fix |
|---|---|---|
| `sisf: NOT_FOUND: no running standalone factory was found for <dir>` (observed, from `stop`, `down` and `down -v`) | the Standalone factory is not running; `down -v` then removes nothing | nothing to stop. To remove the data of a stopped Standalone install, delete `<dir>/data` yourself |

## doctor

| Line | Cause | Fix |
|---|---|---|
| `FAIL manifest  <dir> has no readable factory manifest: run sisf init first` | wrong `--dir` or `$SISF_HOME`, or no install yet | point at the install, or create one |
| `FAIL release manifest  no usable signed release manifest: run sisf up to build and sign one` | the install was never started | `sisf up` |
| `FAIL zones  the control plane does not answer on 127.0.0.1:7463: start the factory with sisf up`; `FAIL state store  no state database at <dir>/data/state/factory.db: ...` | the factory is not running (Standalone) | `sisf up` |
| `FAIL containers  Docker Compose does not answer for project <project>: ...`; several `edge admin`, `docker filters` failures | Docker mode and the project is not running, or `docker` is not on `PATH` | check `docker compose version`, then `sisf up` |
| `OK zone ...` and `OK rollback ... last known good release recorded` next to `FAIL state store  no state database` | another factory on this machine answers on the fixed ports; `doctor` reports its health | run one factory per machine ([why](install.md#one-factory-per-machine)) |
| `FAIL github events  control and the supervisor refuse to start: INVALID_ARGUMENT: the manifest must name the GitHub installation in extensions.github_installation_id` | a real install without its installation id or account | [name them in factory.yaml](install.md#5-name-the-github-installation-and-account) |
| `FAIL github  the token was refused: it is wrong, expired or revoked` | the GitHub token no longer works | a token must stay valid while the factory runs ([real install](install.md#1-github-the-account-and-token-the-factory-acts-as)) |
| `FAIL github  the token cannot see <repo>: grant the app access to the repository (answer 404)` | the account has no access to the repository | grant it |
| `FAIL github  the token can read <repo> but not write: ...` | read-only access | give the account write access to issues, contents and pull requests |
| `WARN github token scope  the token carries the workflow scope, ...` | a classic token with `workflow` | issue a token without it |
| `WARN budget  not confirmed: the factory takes in issues but starts no work; ...` | no budget confirmation | `sisf up --confirm-unlimited --no-alert`, or `--budget-cap` |
| `FAIL delegated keys  the <purpose> key expired at <time>; run sisf up to issue new ones` | the supervisor's delegated keys expired | stop the factory, `sisf up` |
| `FAIL compose isolation  the Compose file is missing: run sisf up to render it` | Docker mode before the first `up` | `sisf up` renders it |
| `FAIL sandbox network  ... is not internal: ...` | the build sandbox network is an ordinary bridge | recreate it with `docker network create --internal` |
| `FAIL fake identity  sign-in points at the fake identity provider on 127.0.0.1:7468, ...` | a real install signs in at the simulation's fake | create the install again with a real provider |

## Commands that call the running factory

| Message | Cause | Fix |
|---|---|---|
| `sisf: UNAUTHENTICATED: no login for http://127.0.0.1:7463; run sisf login or set SISF_TOKEN` (observed, for another URL) | no stored login for that URL | `sisf login` (with the same `--url` and `--dir`) |
| `sisf: UNAVAILABLE: the factory at <url> cannot be reached` | the factory is not running, or `--url` / `$SISF_URL` is wrong | `sisf up`; check the URL |
| `STEP_UP_REQUIRED` from `approvals decide`, `deployments.rollback` or `budget.set` | these run only from their dashboard forms after a fresh sign-in | use the dashboard ([approvals](policy-and-approvals.md#deciding-an-approval)) |
| `NOT_SUPPORTED` on a step-up | the person signed in with GitHub, which cannot confirm a step-up | sign in through the OpenID Connect provider |
| `sisf approvals decide: --decision, --nonce, --subject-digest and --policy-revision are required; "sisf approvals get <id>" shows the last three` (observed) | missing flags | read them with `sisf approvals get <id>` |
| `sisf: NOT_FOUND: manifest .sisf/factory.yaml does not exist` (observed, from `conformance check`) | the default file is the repository's policy copy, relative to the current directory | pass a file: `sisf conformance check <dir>/factory.yaml` |

## Policy and emergency stop

| Message | Cause | Fix |
|---|---|---|
| `POLICY_HOLD: <file> could not be read at <sha>` | a foundation file is missing at that commit, or GitHub cannot be reached | commit all five files; check the GitHub API URL |
| `APPROVAL_REQUIRED: replacing the policy in force needs an approval of the new revision` (observed) | a second revision without an approval | [changing the policy](policy-and-approvals.md#changing-the-policy) |
| `INVALID_ARGUMENT: commit "<sha>" must be a full 40- or 64-character lowercase hash` (observed) | a short SHA | pass the full SHA |
| `sisf: UNAVAILABLE: the emergency stop could not use every route it was given` (observed) | one route failed; the JSON above names it under `failures` | see the [incident runbook](incident.md#1-stop-the-damage) |
| `sisf: INVALID_ARGUMENT: no state store connection was given; set SISF_STATE_DSN` (observed) | `--lift` without the store | set `SISF_STATE_DSN` as the runbook shows |
| `sisf emergency-stop: --factory-id, --reason and an actor (--actor or $USER) are required` (observed) | one of the three is missing; with `$USER` unset, the actor is | pass `--actor` |
