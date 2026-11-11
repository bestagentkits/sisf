# Troubleshooting

Real messages of `sisf`, `sisf doctor` and `factoryd`, what causes them and what fixes them. Messages marked (observed) were produced by running the command; the others are quoted from the code that prints them. For an issue that does not move, start with [Why is my issue stuck?](stuck-issues.md); for an outage, the [incident runbook](incident.md).

`sisf doctor` starts with `sisf <version>, install <dir>`, then prints one line per check: `OK`, `WARN`, `FAIL` or `SKIP`, the check's name and the detail, and a summary of the form `<n> ok, <n> warnings, <n> failures, <n> skipped`. It exits 1 when any check failed and prints nothing more. A `SKIP` follows from an earlier failure, usually `running`: fix that one first. Name the first line in a bug report; `sisf version` prints the same build.

## Installing and starting

| Message | Cause | Fix |
|---|---|---|
| `the value of SISF_STATE_DSN has a character an env file cannot carry safely` | the install directory's path has a character the env files cannot carry (`internal/install/secrets.go`; a Standalone install accepts single spaces); the message names the derived variable, not the path | use a plain path; a failed `init` leaves nothing behind |
| `sisf init: the install directory <dir> already holds N other entries (...); choose a new or empty directory with --dir`, or `1 other entry` (observed, exit 2) | `init` in a directory that holds other files | choose a new or empty directory |
| ``sisf init: no source checkout of sisf was found: run sisf from the checkout, or pass --source with its path; every `sisf up` builds the factory from it`` (observed, exit 2) | `init` ran outside a checkout of this repository | run it from the checkout, or pass `--source` |
| `sisf init: a real install acts for a GitHub App and cannot start without it; pass --github-installation-id N (...) and --github-app-login LOGIN (...)` (observed, exit 2) | a real install without its GitHub App installation and account | pass both flags ([create the install](install.md#4-create-the-install)) |
| `sisf: ALREADY_EXISTS: an install already exists in <dir>` (observed) | `init` on an existing install; nothing was changed | `sisf up --dir <dir>` starts it; to start over, `sisf down -v` and delete `<dir>` |
| `<path> is not a source checkout (no go.mod); pass --source` | the checkout `sisf up` builds from (`--source`, the one recorded in `<dir>/source.json`, else the current directory) is gone or is not a checkout of this repository | pass `--source <checkout>`; for the service units, `sisf service units --dir <dir> --source <checkout>` ([service units](install.md#running-standalone-as-a-service)) |
| ``this install is already running (process N); stop it first with `sisf stop --dir <dir>` `` | a Standalone `sisf up` for this directory holds its run lock (`<dir>/run/sisf.lock`) | use that one, or `sisf stop` first |
| ``this install is already running: units of a `sisf up` that has ended still run (<units>); stop them first with `sisf stop --dir <dir>` `` (from `up`; `restart` says they cannot be restarted without it) | the `sisf up` was killed, but its units hold the run lock they inherited | `sisf stop --dir <dir>`, which stops each unit after checking its pid and start time against `<dir>/run/units/`, then `sisf up` |
| ``the run lock <dir>/run/sisf.lock is held, but by no `sisf up` of this install and no unit it recorded: ...; nothing was signalled or removed`` | a program a unit started kept the lock open, or a `sisf up` of an older sisf runs, which records itself in the pid file only | `lsof <dir>/run/sisf.lock`, stop what it lists, and try again; nothing was signalled or removed |
| ``removed the stale pid file <dir>/run/sisf.pid (it named process N, which is not a `sisf up` of this install)`` | a pid file left by a crash or from before a reboot; no `sisf up` holds the run lock, so the file is removed and the process it names is never signalled | nothing to do |
| `sisf: CONFLICT: <dir>/run/sisf.pid names process N, which is alive, but this install has no run lock (...)` | after an upgrade from a `sisf` without the run lock, or after a reboot gave the pid to another process; nothing was signalled or removed | `ps -p N -o command`: if it is this install's `sisf up`, stop it and start again with the new `sisf`; if not, remove the pid file |
| `cannot lock <dir>/run/sisf.lock; the install directory must be on a file system that supports flock` | the install directory is on a file system without `flock` | move the install to a local file system |
| `this install runs in <mode> mode; the mode is fixed when it is created`, `the simulation profile is fixed when the install is created` | `up` was given a `--mode` or `--profile` other than the install's | drop the flag; make a new install for another mode |
| `the budget is already confirmed; it is confirmed once` | a second budget confirmation at `up` | change it through the dashboard ([budget](policy-and-approvals.md#budget)) |
| `init` prints `warning: The budget is not confirmed: the factory takes in issues and starts no work until you confirm it ...` (observed), or at a terminal asks `Type "unlimited" to confirm it:` | no budget flag was given; `init` asks only when standard input is a terminal | pass `--confirm-unlimited --no-alert` (or `--alert-threshold`), or `--budget-cap`, now or at the first `sisf up` |
| `warning: Service units were not written: ...` from `init` (observed for a missing `go`) | `init` ran from a temporary `go run` build, or `go` was not on `PATH`; the warning names which | build `sisf` with `make install`, or put Go on `PATH`, then `sisf service units --dir <dir>` |
| `sisf: CONFLICT: the Compose project <name> belongs to another install ...; nothing was changed` | Docker mode: another install of the same factory id has containers on this host | make this install again with `--factory-id` |
| `sisf: CONFLICT: volume <volume> of the Compose project <name> belongs to the install in <dir>; nothing was changed ...` | Docker mode: the project's volumes carry the `sisf.install-dir` label of another install (`internal/install/docker_project_owner.go`) | make this install again with `--factory-id`, or remove the other install's data with `sisf down -v --dir <other dir>` if it is no longer needed |
| `sisf: CONFLICT: volume <volume> of the Compose project <name> was left by an install this directory does not know; nothing was changed, so its data is not adopted ...` | Docker mode: unlabelled volumes of that project exist and this install never started its project | list them with `docker volume ls --filter label=com.docker.compose.project=<name>`; remove them only if no install needs their data, or use `--factory-id` |
| `--dev and --profile sim are different profiles; choose one`, `--budget-cap and --confirm-unlimited are two different budgets; choose one` (observed, exit 2) | contradictory flags | follow the message |
| the supervisor refuses with `PERMISSION_DENIED`, reported by `sisf up` | a Standalone install outside the simulation with `--exec local`: candidate builds are held by design | add a [Docker build sandbox](install.md#building-in-a-docker-sandbox) |

## Stopping and removing

| Message | Cause | Fix |
|---|---|---|
| `the factory in <dir> was not running; nothing to stop` (observed, from `stop` and `down`, exit 0) | Standalone: the factory is already stopped, and no unit of it lives | nothing to do; `down -v` on a stopped install still removes the data and says `...; its data is removed` |
| `the factory in <dir> was never started; nothing to stop`, or from `down -v` `...was never started; nothing was removed` (exit 0) | Docker: this install never made its Compose project. `down -v` adds which install owns volumes of the same project name, when there are any | nothing to do; if the volumes are another install's, see the Docker project messages above |
| `the factory in <dir> was not running; nothing to stop` (from `stop` and `down` in Docker mode as well, exit 0) | Docker: the project exists but nothing of it runs | nothing to do |
| `sisf: NOT_FOUND: the factory in <dir> is not running` (observed, from `restart`, exit 1) | `restart` acts only on a running factory | `sisf up --dir <dir>` |
| `sisf: NOT_FOUND: <dir> holds no install (no factory.yaml)` (observed) | wrong `--dir` or `$SISF_HOME` | point at the install |

## doctor

| Line | Cause | Fix |
|---|---|---|
| ``FAIL manifest  <dir> holds no install (no factory.yaml): run `sisf init --dir <dir>` first`` (observed) | wrong `--dir` or `$SISF_HOME`, or no install yet | point at the install, or create one |
| ``FAIL running  the factory is not running (no `sisf up` process of this install): start it with `sisf up --dir <dir>` ``, followed by `SKIP` lines | the factory is not running; in Docker mode the reason names the Compose project or a missing `docker` | `sisf up`; in Docker mode check `docker compose version` first |
| `FAIL running  this install is not running (...), yet port 7463 is answered by factory <id>, not this install's <id>; ...` | another factory on this machine holds the fixed ports | stop that factory first; one factory per machine ([why](install.md#one-factory-per-machine)) |
| `FAIL running  this install runs, but ... is answered by factory <id> ...` | this install's `sisf up` runs, yet another factory answers on the ports | stop the other one, then `sisf restart --dir <dir>` |
| ``WARN running  the `sisf up` process of this install runs, but not every unit answered with this install's identity; see the lines below`` | Standalone: a unit is down | read the `zones` and `rollback` lines |
| `FAIL state store  no state database at <dir>/data/state/factory.db: ...` | the factory runs but its store is missing | check that `--dir` names this install; `sisf up` creates the database |
| `FAIL github events  control and the supervisor refuse to start: INVALID_ARGUMENT: the manifest must name the GitHub installation in extensions.github_installation_id` | an install made before `init` took the GitHub App flags, or a hand-edited `factory.yaml` | make the install again with `--github-installation-id` and `--github-app-login` ([create the install](install.md#4-create-the-install)) |
| `FAIL github  the token was refused: it is wrong, expired or revoked` | the GitHub token no longer works | replace the token; an App installation token expires after an hour and nothing renews it in this release ([real install](install.md#1-github-an-app-and-the-token-the-factory-acts-as)) |
| `FAIL github  the token cannot see <repo>: grant the app access to the repository (answer 404)` | the account has no access to the repository | grant it |
| `FAIL github  the token can read <repo> but not write: ...` | read-only access | give the account write access to issues, contents and pull requests |
| `WARN github token scope  the token carries the workflow scope, ...` | a classic token with `workflow` | issue a token without it |
| `WARN budget  not confirmed: the factory takes in issues but starts no work; ...` | no budget confirmation | `sisf up --confirm-unlimited --no-alert`, or `--budget-cap` |
| `FAIL delegated keys  the <purpose> key expired at <time>; run sisf up to issue new ones` | the supervisor's delegated keys expired | stop the factory, `sisf up` |
| `FAIL sandbox network  ... is not internal: ...` | the build sandbox network is an ordinary bridge | recreate it with `docker network create --internal` |
| `FAIL fake identity  sign-in points at the fake identity provider on 127.0.0.1:7468, ...` | a real install signs in at the simulation's fake | create the install again with a real provider |

## Commands that call the running factory

| Message | Cause | Fix |
|---|---|---|
| `sisf: UNAUTHENTICATED: no login for http://127.0.0.1:7463; run sisf login or set SISF_TOKEN` (observed, for another URL) | no stored login for that URL | `sisf login` (with the same `--url` and `--dir`) |
| `sisf: UNAVAILABLE: the factory at <url> cannot be reached` | the factory is not running, or `--url` / `$SISF_URL` is wrong | `sisf up`; check the URL |
| `sisf: CONFLICT: the server at <url> is factory <id>, not <id> of the install in <dir>; nothing was sent to it` | `call`, `login` or a shortcut reached another factory than the install in `--dir` or `$SISF_HOME`; no token or login request was sent | name this factory's URL with `--url`, or stop the other factory; `sisf doctor` shows who answers |
| `sisf: CONFLICT: the server at <url> does not say which factory it is (HTTP <code>), so nothing was sent to it` | what answers on that URL is not a factory of this release | check the URL; `sisf doctor` |
| `sisf: CONFLICT: the server at <url> says it is factory <id> but does not name the root key of its install, so it cannot be told from another install of that factory; nothing was sent to it` | the factory was started by an older `sisf`, whose `/healthz` names no root key | restart it with `sisf up` from the current `sisf` |
| `sisf: CONFLICT: the server at <url> is another install of factory <id> (root key <key id>, not the key of the install in <dir>); nothing was sent to it` | a second install of the same factory id answers on the URL | name this install's URL, or stop the other install |
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
| ``store: UNAVAILABLE: the state store is not prepared; run `sisf up`, which migrates it`` (observed, under `failures`) | the install was never started, so it has no state store | nothing to stop yet |
| `sisf emergency-stop: missing the reason: pass --reason`, `missing who asks: pass --actor, since $USER is not set`, `missing the state store, which a lift needs: pass --dir of the install or set $SISF_STATE_DSN` (observed, exit 2) | an input is missing; the message names each one | pass what it names |
| `sisf emergency-stop: --factory-id <id> does not match the install in --dir, which is factory <id>` (observed, exit 2) | `--factory-id` and `--dir` name different factories | drop one |
| `sisf emergency-stop: missing a route to the factory: pass --dir of the install, set $SISF_STATE_DSN or give the supervisor's address with --admin` (observed, exit 2) | `--factory-id` with `--admin none` and no `$SISF_STATE_DSN` | give the store or the admin address |
| `sisf: NOT_SUPPORTED: the install in <dir> runs in Docker mode: ...` | `emergency-stop` cannot reach a Docker install's store and admin socket from the host | `sisf call factory.emergency_stop`, or the dashboard ([incident runbook](incident.md#docker-compose)) |
| `supervisor: CONFLICT: the supervisor that answers serves factory "<id>", not "<id>"; nothing was sent to it` | another factory's supervisor holds the admin address | stop the right factory; one factory per machine ([why](install.md#one-factory-per-machine)) |

## Releases

| Message | Cause | Fix |
|---|---|---|
| `sisf runs explain` answers `release_refused`, with a reason that says the change touches the trusted control surface or a unit outside `release.units` | the candidate is a build of a unit that `release.units` in the install's `factory.yaml` does not name, or changes the trusted control surface; the manifest `sisf init` writes names `app` alone (`internal/supervisor/release/governance.go`). The supervisor logs `release refused before anything was deployed: ...` at warning level with the full reason, such as `APPROVAL_REQUIRED: unit "<unit>" is not one release.units lets the factory publish by itself; ...` | nothing is deployed and the issue is blocked; see [Why is my issue stuck?](stuck-issues.md#an-issue-that-stays-in-releasecandidate). Such a change is published only in a governance update a person makes |
| `sisf runs explain` answers `blocked` with evidence `blocked with cause release_interrupted` or `release_unjudged` | a release was restored to the last known good release because no supervisor watched it to its end, or it could not be judged | read the recovery report, then retry ([causes of a block](stuck-issues.md#causes-of-a-block)) |
| the supervisor does not start: `the synthetic suite <path> cannot be found`, `... is not a regular file` or `... is not executable` | `--synthetic-program` names a program that is missing or cannot be run | rebuild with `sisf up`, which builds `bin/hello-app-synthetic` |
| an issue stays in Verified, and its progress comment says what the release waits on | a release held after it was verified (an emergency stop, GitHub, the token, the lease) is finished by a later pass: its `finish-due/<run>` record says what it still owes | do not close it by hand; lift the stop or fix the cause the comment names ([Why is my issue stuck?](stuck-issues.md)) |
| `sisf runs explain` answers `release_refused` with a reason that says the candidate builds the same program as the one serving | the program is identical to the serving one, so there is nothing to deploy | review the candidate's pull request and merge it by hand |
| `report_publish_error` in `sisf status` says `PERMISSION_DENIED` about `sisf:report`, or `reports_publish_paused_until` is set | the factory's GitHub token may open issues but not label them; or 50 report issues were opened in the last 24 hours, and publishing pauses with nothing dropped | give the token permission to label issues; the pause lifts by itself ([public bug reports](operations.md#public-bug-reports)) |
