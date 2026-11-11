# CLI reference

Every `sisf` command, its flags and where it is used. The command line is the authority: `sisf help` lists the commands and `sisf help <command>` (for a pair such as `runs explain`, `sisf help runs explain`) prints the full text. Some commands, such as `init` and `call`, print a list of their flags there; others, such as `emergency-stop`, `doctor` and `down`, name them only in their usage line. This page groups the commands and adds those flags, read from the flag definitions in `cmd/sisf/`.

Conventions of every command (`cmd/sisf/main.go`): exit 0 on success, 1 on a failure, 2 on a wrong invocation. A wrong invocation (an unknown command or flag, a missing argument, an option `init` refuses) prints `sisf <command>: message` and the command's usage line. A failure prints `sisf: CODE: message`, often followed by `next:` lines. A few invalid values are noticed only later and exit 1 with `INVALID_ARGUMENT:`, for example `sisf up --mode docker` on a Standalone install. Secrets are never taken from a flag value and never printed.

## Version

| Command | What it does |
|---|---|
| `sisf version [--json]`, `sisf --version`, `sisf -v` | Prints the version, the commit, the build date, the Go version and the platform of this `sisf`, for example `sisf <version> (commit <sha>, built <date>, <go version> <os>/<arch>)`. `--json` prints one object with `version`, `commit`, `build_date`, `modified` (only when the build had local changes), `go_version` and `platform`. A `sisf` built by `make install` carries its version; one from a plain `go build` or `go install` reports `devel`. |

The top-level `-v` is the version; `sisf down -v` is the flag that removes data.

## Install and lifecycle

Owner: `cmd/sisf/cmd_install*.go`, `cmd/sisf/cmd_service_units.go`, `internal/install`. Procedures: [installing](install.md), [maintenance](maintenance.md).

| Command | What it does |
|---|---|
| `sisf init --repo owner/name (--github-token-env NAME --github-installation-id N --github-app-login LOGIN \| --profile sim \| --dev --github-credential gh-cli) [sign-in flags] [--mode standalone\|docker] [--dir path]` | Creates an install in a new or empty directory: `factory.yaml`, one credential set per zone, a root signing key, the budget confirmation, `source.json` (the source checkout) and, in Standalone mode, service unit files. Builds and starts nothing. Refuses a directory that holds other files, and refuses without a source checkout of sisf (`--source`, else the checkout holding the current directory). Every option is checked before anything is written; a refused one exits 2 and leaves nothing behind. It asks for the budget only when standard input is a terminal. |
| `sisf up [--dir path] [init flags]` | Builds the programs or images from the source checkout, signs the release manifest, checks isolation, prepares the state store and starts every unit. Creates the install first when the directory holds none. Standalone stays in the foreground; Docker starts one Compose project and returns. |
| `sisf stop [--dir path]` | Stops the factory and keeps everything (`docker compose stop` in Docker mode). |
| `sisf restart [--dir path]` | Restarts every unit (Standalone: the same as sending SIGHUP to `sisf up`). On a stopped Standalone install it answers `NOT_FOUND` and exits 1. |
| `sisf down [--dir path] [-v]` | Stops the factory; Docker mode also removes the project's containers and networks. `-v` (or `--volumes`) also removes the data: the project's volumes in Docker mode (`docker compose down --volumes`), `<dir>/data` in Standalone. Secrets and `factory.yaml` are never removed. |
| `sisf doctor [--dir path] [--json]` | Checks the install end to end and changes nothing. Prints `sisf <version>, install <dir>`, then one `OK`, `WARN`, `FAIL` or `SKIP` line per check and a summary; exits 1 on any failure. Its `running` check proves the factory answering on the fixed ports is this install's; when it is not running, or another factory answers, the live checks are skipped. `--json` prints the same report as one object: `dir`, `sisf_version`, `ok`, `counts`, `checks`. |
| `sisf service units [--dir path] [--source path]` | Writes the systemd and launchd units of an existing Standalone install again, into `<dir>/service/`, for when `init` skipped them or after `sisf` or the checkout moved. The units run the `sisf` that wrote them. |

`--dir` defaults to `$SISF_HOME`, else `~/.sisf/factory`.

On a factory that is not running, `stop`, `down` and `down -v` exit 0 and print `the factory in <dir> was not running; nothing to stop` (`down -v`: `...; its data is removed`). In Docker mode `up`, `stop`, `restart` and `down` refuse a Compose project of this install's name that another install made, and change nothing.

Flags of `init`, which `up` also accepts (`cmd/sisf/cmd_install_flags.go`):

| Flag | Meaning |
|---|---|
| `--dir` | install directory |
| `--mode` | `standalone` (default) or `docker`; fixed when the install is created |
| `--profile sim` | run against the fake GitHub and the fake identity provider |
| `--repo` | GitHub repository the factory serves, `owner/name` |
| `--factory-id` | factory id; default `sisf-<repository name>` (also the Compose project name) |
| `--source` | source checkout to build from; default the one `init` recorded, else the checkout holding the current directory |
| `--github-token-env` | name of the environment variable holding the GitHub token; the factory reads it once per start and never refreshes it, so it must be long-lived |
| `--github-installation-id`, `--github-app-login`, `--github-app-id` | the GitHub App installation whose webhooks control accepts, the account the factory writes as (`name[bot]`) and, optionally, that account's numeric id. The first two are required outside `--profile sim` and `--dev --github-credential gh-cli`, and refused with `--profile sim`. |
| `--github-api-url` | GitHub API root (https, or http on loopback) |
| `--github-reconcile-interval` | how often control asks GitHub for changed issues, 10s to 1h (default 1m) |
| `--debug` | Docker mode: publish the state store on 127.0.0.1:7469 for this `up` |
| `--confirm-unlimited`, `--alert-threshold`, `--no-alert` | confirm the unlimited budget, with an alert amount or no alert |
| `--budget-cap`, `--run-estimate`, `--alert-threshold` | confirm a capped budget; `--run-estimate` is what each run sets aside (default a fifth of the cap) |

Sign-in flags (`cmd/sisf/cmd_install_flags_identity.go`): `--external-url`, `--owner issuer#subject` (repeatable), `--oidc-issuer`, `--oidc-client-id`, `--oidc-client-secret-file` or `--oidc-client-secret-env`, `--github-login-client-id` with `--github-login-client-secret-file` or `--github-login-client-secret-env`, `--github-login-url`, `--github-login-api-url`, `--dev`, `--github-credential gh-cli`.

Agent and build flags (`cmd/sisf/cmd_install_flags_agents.go`): `--coder-agent fake|claude|codex` (a real agent runs only in Standalone mode), `--coder-model`, `--workspace-source`, `--qa-judge agent`, `--qa-agent claude|codex|pi`, `--qa-model`, `--qa-agent-binary` (needed by Docker mode and by a Standalone build sandbox), `--build-command`, `--exec local|docker`; Standalone only: `--agent-home`, `--qa-home`, and for a Docker build sandbox `--exec-docker-endpoint`, `--exec-docker-network`, `--egress-proxy`, `--runner-image`. See [real agents](install.md#real-agents-in-standalone).

## Inspecting and operating a running factory

These commands call the running factory's REST surface (`cmd/sisf/cmd_call.go`). Each takes the connection flags `--url` (default `$SISF_URL`, the stored login, or `http://127.0.0.1:7463`), `--dir` (where the stored login is) and `--allow-insecure-http` (send credentials over plain http to a non-loopback host). The token comes from `$SISF_TOKEN`, else from `sisf login`. When `--dir` or `$SISF_HOME` holds an install, they first ask the server which factory it is and send nothing, the token included, to one that is not this install's: the answer is `CONFLICT: the server at <url> is factory <id>, not <id> of the install in <dir>; nothing was sent to it` (`cmd/sisf/cmd_call_factory_identity.go`).

| Command | Same as | Notes |
|---|---|---|
| `sisf login [--url url] [--dir path] [--name label] [--scope scope]...` | | Device-style login: open the printed URL, check the code, approve. Stores a short-lived token owner-only in `credentials.json` under the SISF home. Approving needs a recent identity confirmation, which the page offers. |
| `sisf status` | `sisf call factory.status` | issues per state, stopped issues, open approvals, GitHub reconciliation |
| `sisf issues [--state s]... [--limit n]` | `sisf call issues.list` | `--state` takes a lifecycle state such as `Blocked` (repeatable) |
| `sisf runs inspect <issue\|run-id>` | `sisf call runs.inspect` | an issue is `owner/repo#N`; anything else is a run id |
| `sisf runs explain <issue\|run-id>` | `sisf call runs.explain` | why the run is stopped, with evidence and next steps |
| `sisf approvals list [--status s]... [--limit n]` | `sisf call approvals.list` | |
| `sisf approvals get <id>` | `sisf call approvals.get` | shows the nonce, subject digest and policy revision a decision must name |
| `sisf approvals decide <id> --decision d --nonce n --subject-digest d --policy-revision r [--reason text] [--dry-run]` | `sisf call approvals.decide` | Also takes `--budget-cap` (micros) and `--expires-at` (RFC 3339) as conditions, and `--idempotency-key`. Without `--dry-run` it answers `STEP_UP_REQUIRED`: a decision runs only from the dashboard form. See [approvals](policy-and-approvals.md#deciding-an-approval). |
| `sisf call <action> [--input json\|-] [--dry-run] [--approval id] [--idempotency-key key] [--url url]` | | Calls any action and prints its JSON result. The actions are the operations of [`api/openapi.yaml`](../../api/openapi.yaml) (`/api/v1/actions/<action>`). |
| `sisf call --list [--json]` | | Lists every action this `sisf` knows, with its guard (`read`, `write`, `sensitive`, `step-up`) and summary, without a running factory. |

Actions without a shortcut, such as `budget.get` or `setup.sample_issue`, are called with `sisf call`; `sisf call --list` names them all.

## Emergency stop

| Command | What it does |
|---|---|
| `sisf emergency-stop --reason text [--dir path \| --factory-id id] [--actor id] [--admin addr] [--state-dsn-env NAME] [--status \| --lift]` | Records the stop in the state store, then asks the supervisor on its admin address to cancel runs and revoke their secrets. Prints what each route did as JSON; exits 1 when a route it was given failed. |

Flags (`cmd/sisf/cmd_emergency_stop.go`):

| Flag | Meaning |
|---|---|
| `--dir` | install to stop (default `$SISF_HOME`, else `~/.sisf/factory`). The factory id comes from its `factory.yaml`, the state store from the control zone's secrets, and the admin address is `127.0.0.1:7464`. A Docker install is refused with `NOT_SUPPORTED`: its store and admin socket are inside the Compose project, so use `sisf call factory.emergency_stop` or the dashboard. |
| `--factory-id` | factory to stop when it is not installed on this host; then the store comes from `$SISF_STATE_DSN` (or the variable `--state-dsn-env` names) and the admin address from `--admin`. Beside `--dir`, it must match the install's id. |
| `--admin` | supervisor admin address: `unix:/absolute/path` or a loopback `host:port`; `none` skips the supervisor route |
| `--actor` | who asks, for the audit log (default `user:$USER`) |
| `--reason` | why the factory is stopped or the stop is lifted |
| `--status` | ask the supervisor only whether a stop is in force; needs no `--reason` |
| `--lift` | lift the stop through the store; the supervisor socket cannot lift one |

Before anything is sent to the supervisor, it must prove that it serves this factory (and, with an install, this install's root key); otherwise the answer is `CONFLICT: the supervisor that answers serves factory "<id>", not "<id>"; nothing was sent to it`. A supervisor also refuses a stop for a factory other than its own. The procedure, per mode, is in the [incident runbook](incident.md#1-stop-the-damage).

## Manifests

| Command | What it does |
|---|---|
| `sisf conformance check [file]` | Checks one factory manifest against the deployment rules; exits 0 only when it is conformant. The default file is `.sisf/factory.yaml` in the current directory, the repository's policy copy. To check an install, pass `<dir>/factory.yaml`. |

## Run by the factory itself

Listed by `sisf help` but started by `sisf up` or its Compose project, not by hand: `sisf state bootstrap`, `sisf trust install`, `sisf docker-filter` (also started by the operator for a [Standalone build sandbox](install.md#building-in-a-docker-sandbox)), `sisf sim github`, `sisf sim oidc`.

## Owner commands on the factory host: `factoryd`

Some Owner operations have no `sisf` command yet. They are subcommands of `factoryd`, the control and dispatcher program, run on the factory's host with the control zone's environment (`cmd/factoryd/main.go`, `cmd/factoryd/roles.go`):

| Command | What it does |
|---|---|
| `factoryd policy approve --revision SHA [--approval ID]` | Puts the foundation files at commit `SHA` in force as the policy. The first revision needs nothing more; replacing one needs `--approval`. |
| `factoryd policy request --revision SHA` | Files the approval request that replacing the policy needs and prints its id. |
| `factoryd issue retry --issue owner/repo#N` | Sends a Blocked issue back to Ready. |

How to give them the control zone's environment in each mode: [running Owner commands](policy-and-approvals.md#running-owner-commands-on-the-host). The supervisor program has a read-only `status` subcommand (`cmd/supervisor/main.go`) that prints its self-check, last known good release and held builds as JSON.
