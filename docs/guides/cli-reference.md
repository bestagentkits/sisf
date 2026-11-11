# CLI reference

Every `sisf` command, its flags and where it is used. The command line is the authority: `sisf help` lists the commands and `sisf help <command>` (for a pair such as `runs explain`, `sisf help runs explain`) prints the full text. This page adds the flags the help text does not print yet, read from the flag definitions in `cmd/sisf/`.

Conventions of every command (`cmd/sisf/main.go`): exit 0 on success, 1 on a failure, 2 on a wrong invocation (an unknown command or flag, a missing argument). Some invalid flag values still exit 1 with an `INVALID_ARGUMENT:` message instead of 2. A failure prints `sisf: CODE: message`, often followed by `next:` lines. Secrets are never taken from a flag value and never printed.

## Install and lifecycle

Owner: `cmd/sisf/cmd_install*.go`, `internal/install`. Procedures: [installing](install.md), [maintenance](maintenance.md).

| Command | What it does |
|---|---|
| `sisf init --repo owner/name (--github-token-env NAME \| --profile sim \| --dev --github-credential gh-cli) [sign-in flags] [--mode standalone\|docker] [--dir path]` | Creates an install: `factory.yaml`, one credential set per zone, a root signing key, the budget confirmation and, in Standalone mode, service unit files. Builds and starts nothing. |
| `sisf up [--dir path] [init flags]` | Builds the programs or images from the source checkout, signs the release manifest, checks isolation, prepares the state store and starts every unit. Creates the install first when the directory holds none. Standalone stays in the foreground; Docker starts one Compose project and returns. |
| `sisf stop [--dir path]` | Stops the factory and keeps everything (`docker compose stop` in Docker mode). |
| `sisf restart [--dir path]` | Restarts every unit (Standalone: the same as sending SIGHUP to `sisf up`). |
| `sisf down [--dir path] [-v]` | Stops the factory; Docker mode also removes the project's containers and networks. `-v` (or `--volumes`) also removes the data: the state volume in Docker mode, `<dir>/data` in Standalone. Secrets and `factory.yaml` are never removed. |
| `sisf doctor [--dir path]` | Checks the install end to end and changes nothing. Prints one `OK`, `WARN` or `FAIL` line per check and exits 1 on any failure. |

`--dir` defaults to `$SISF_HOME`, else `~/.sisf/factory`.

Flags of `init`, which `up` also accepts (`cmd/sisf/cmd_install_flags.go`):

| Flag | Meaning |
|---|---|
| `--dir` | install directory |
| `--mode` | `standalone` (default) or `docker`; fixed when the install is created |
| `--profile sim` | run against the fake GitHub and the fake identity provider |
| `--repo` | GitHub repository the factory serves, `owner/name` |
| `--factory-id` | factory id; default `sisf-<repository name>` (also the Compose project name) |
| `--source` | source checkout to build from; default the current directory |
| `--github-token-env` | name of the environment variable holding the GitHub token |
| `--github-api-url` | GitHub API root (https, or http on loopback) |
| `--github-reconcile-interval` | how often control asks GitHub for changed issues, 10s to 1h (default 1m) |
| `--debug` | Docker mode: publish the state store on 127.0.0.1:7469 for this `up` |
| `--confirm-unlimited`, `--alert-threshold`, `--no-alert` | confirm the unlimited budget, with an alert amount or no alert |
| `--budget-cap`, `--run-estimate`, `--alert-threshold` | confirm a capped budget; `--run-estimate` is what each run sets aside (default a fifth of the cap) |

Sign-in flags (`cmd/sisf/cmd_install_flags_identity.go`): `--external-url`, `--owner issuer#subject` (repeatable), `--oidc-issuer`, `--oidc-client-id`, `--oidc-client-secret-file` or `--oidc-client-secret-env`, `--github-login-client-id` with `--github-login-client-secret-file` or `--github-login-client-secret-env`, `--github-login-url`, `--github-login-api-url`, `--dev`, `--github-credential gh-cli`.

Agent and build flags, Standalone only (`cmd/sisf/cmd_install_flags_agents.go`): `--coder-agent fake|claude|codex`, `--coder-model`, `--agent-home`, `--workspace-source`, `--qa-judge agent`, `--qa-agent claude|codex|pi`, `--qa-model`, `--qa-home`, `--qa-agent-binary`, `--build-command`, `--exec local|docker`, and for a Docker build sandbox `--exec-docker-endpoint`, `--exec-docker-network`, `--egress-proxy`, `--runner-image`. See [real agents](install.md#real-agents-in-standalone).

## Inspecting and operating a running factory

These commands call the running factory's REST surface (`cmd/sisf/cmd_call.go`). Each takes the connection flags `--url` (default `$SISF_URL`, the stored login, or `http://127.0.0.1:7463`), `--dir` (where the stored login is) and `--allow-insecure-http` (send credentials over plain http to a non-loopback host). The token comes from `$SISF_TOKEN`, else from `sisf login`.

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

Actions with no shortcut today: `budget.get`, `budget.set`, `secrets.list_refs`, `factory.emergency_stop`, `deployments.rollback`, `setup.sample_issue`, `telemetry.search`, `features.list`, `changelog.list`, `docs.list`, `decisions.propose_update`. Call them with `sisf call`, for example `sisf call budget.get`.

## Emergency stop

| Command | What it does |
|---|---|
| `sisf emergency-stop --factory-id id --reason text [--actor id] [--admin addr] [--state-dsn-env NAME] [--status \| --lift]` | Records the stop in the state store (from `$SISF_STATE_DSN` or the variable `--state-dsn-env` names), then asks the supervisor on `--admin` (default `127.0.0.1:7464`; `unix:/path` or a loopback `host:port`; empty skips it) to cancel runs and revoke their secrets. Prints JSON; exits 1 when a route it was given failed. |

`--actor` defaults to `user:$USER`. `--status` asks the supervisor only and needs neither `--factory-id` nor `--reason`. `--lift` uses the store only. The procedure, per mode, is in the [incident runbook](incident.md#1-stop-the-damage).

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
