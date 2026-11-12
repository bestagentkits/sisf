# Installing a factory

How to install a factory beyond the simulation of the [quickstart](quickstart.md): on your own machine against a real repository (development install), and for real (a GitHub App or account, a real sign-in provider, an HTTPS origin). Day-to-day operation is in the [operations guide](operations.md); upgrades and backups in [maintenance](maintenance.md).

## Before you choose

| | Standalone | Docker Compose |
|---|---|---|
| Runs as | local processes under one foreground `sisf up` | one Compose project of containers; `sisf up` returns |
| Trust model | trusted code only: agents and units run as your user, unconfined ([security guide](security.md#trust-model-of-each-mode)) | one container per unit with per-zone networks and credentials; no container sandboxing of untrusted code in this release |
| State store | SQLite file in `<dir>/data/state/factory.db` | PostgreSQL container, data in the `<project>-state-data` volume |
| Real coding agents | yes, as local processes | no |
| `--dev` install | yes | refused |

Both are supported operating modes; nothing else is ([capability matrix](../capability-matrix.md)).

Requirements for every install:

- Linux or macOS. `sisf` does not build for Windows (`internal/install/exec.go` uses Unix process groups).
- A checkout of this repository and Go 1.25: `sisf up` builds the factory from source every time it starts. `init` looks for the checkout from the current directory upwards (or takes `--source`), refuses without one, and records it in `<dir>/source.json` for later starts and the service units (`internal/install/source_record.go`).
- `sisf` built once and kept at a stable path: `make install` in the checkout installs it into `$(go env GOPATH)/bin`, stamped with its version, commit and build date (`Makefile`); `sisf version` prints them. A plain `go install ./cmd/sisf` also works but reports its version as `devel`. Do not run `sisf init` from `go run`: the service units name the running executable, so `init` skips them for such a build.
- Docker mode, or a Standalone build sandbox: Docker with Compose.
- Free ports 7460-7469 on 127.0.0.1 (`internal/install/options.go`).
- A new or empty install directory, on a file system that supports `flock`: `init` refuses one that holds other files, and a Standalone `sisf up` holds a lock on `<dir>/run/sisf.lock` and passes it to every unit it starts, so the install counts as running while any unit lives, also after that `sisf up` was killed (on Linux the units of a killed `sisf up` are told to stop when it dies; elsewhere they keep running), and a second `up` of the same install is refused until `sisf stop --dir <dir>` has stopped what is left (`internal/install/run_lock.go`, [operations](operations.md#lifecycle)).

### One factory per machine

The ports are fixed, so one machine runs one factory. `init` does not check this, and a second install's units cannot bind the ports. The commands refuse to mistake one factory for another:

- `sisf doctor` first checks that the factory answering on the ports is this install's; when another one answers, its `running` check fails, names that factory and skips the live checks (`internal/install/doctor_answering_factory.go`).
- `sisf call`, `sisf login` and the commands built on `call` send nothing to a server that does not say it is the factory of the install in `--dir` or `$SISF_HOME` (`cmd/sisf/cmd_call_factory_identity.go`).
- `sisf emergency-stop` sends a stop only to a supervisor that proves it serves this factory, and a supervisor refuses a stop for any factory but its own (`internal/supervisor/admin_emergency_stop.go`).

In Standalone mode each factory unit may start only its own binary from the install's `bin/`: control and dispatcher `bin/factoryd`, edge `bin/sisf-edge`, runner `bin/sisf-runner`, qa-runner `bin/sisf-qa-runner`. The application unit may start the application launcher only without arguments, since the launcher runs the program its argument names. The `state` and `artifacts` units start no program of `bin/`, a command word with whitespace around the file name it names is refused, and `sisf-supervisor` always counts as the factory's binary, so the application unit may not start it. `sisf conformance check` refuses a manifest that does otherwise (`internal/conformance/rules_launchers.go`, `rules_standalone_executables.go`).

Two Docker installs of repositories with the same name share a Compose project name (`sisf-<repository name>`). The second one's `up`, `stop`, `restart` and `down` refuse to touch a project another install made (`internal/install/docker_project_owner.go`); give the second install its own `--factory-id`.

## Development install

`--dev` is a Standalone install on loopback for one person on their own machine, against a real repository, signing in at the fake identity provider. It needs a GitHub CLI login with write access to the repository:

```sh
sisf init --dev --mode standalone --dir "$HOME/.sisf/dev" --repo owner/app \
  --github-credential gh-cli --budget-cap 5
```

Expected output (paths shortened):

```text
factory sisf-app created in <dir>
sign in at http://127.0.0.1:7463 as the Owner http://127.0.0.1:7468#owner
source checkout <checkout>: every `sisf up` of this install builds its programs from it
warning: The root signing key is stored in <dir>/secrets/control. ...
warning: DEVELOPMENT INSTALL: the Owner signs in through the fake identity provider on 127.0.0.1:7468, without a password; ...
next: sisf up --dir <dir>
```

Run `init` from the checkout. Then `sisf up --dir "$HOME/.sisf/dev"`, which builds from the checkout `init` recorded, and in another terminal `sisf doctor --dir "$HOME/.sisf/dev"`.

- `--github-credential gh-cli` takes the token from `gh auth token` at `init` and at every `up`; it is accepted only with `--dev`, and such an install needs no GitHub App flags.
- Such an install has no GitHub App installation: the factory acts as your gh login, takes no webhooks (`/webhooks/github` is not served) and learns of issues only through the control zone's reconciliation with GitHub (`cmd/factoryd/wiring/control_reconcile.go`; default every minute; `--github-reconcile-interval 15s` sets it, 10s to 1h). This exception holds in Standalone mode only. Control starts only if GitHub confirms the token may write to the repository, and that access stands in for the installation in role checks. Control checks the access again every 5 minutes (`cmd/factoryd/wiring/control_operator_token_access.go`). If it is revoked or downgraded to read, control's passes do nothing on GitHub and `sisf status` shows `github.token_access: revoked` until a later check confirms it again. A check that cannot reach GitHub, or that GitHub rate-limits, changes nothing. The release supervisor does not run this check and keeps using the token.
- Sign-in is locked, because real agents run as local processes beside the fake identity provider and can reach every loopback port. Each `sisf up` writes a new secret to `<dir>/secrets/control/dev-sign-in` (owner-only, never in a unit's environment, command line or log) and prints where it is; open `http://127.0.0.1:7468/_sim/unlock` and enter it once per browser, then sign in at `http://127.0.0.1:7463`. Without it the fake refuses every sign-in and every `/_sim` endpoint (`cmd/sisf/cmd_sim_oidc_sign_in_gate.go`). The browser keeps an HMAC of the secret in an HttpOnly, `SameSite=Strict` cookie, never the secret, and the fake takes a POST to `/_sim` only with an `Origin` naming its own origin. Use `127.0.0.1` for both the factory and the fake, as printed: the Strict cookie is not sent when a sign-in starts from another site such as `localhost`. The simulation profile stays unlocked.
- `--dev` is refused in Docker mode, with `--profile sim`, with a sign-in provider and with an external URL off loopback. `init` and `doctor` print a loud warning: anyone who can read the sign-in secret, which includes an agent running as your user under `trusted_code_only`, signs in as the Owner.
- Nothing runs until the policy is in force: [put the first policy in force](policy-and-approvals.md#putting-the-first-policy-in-force) from a commit of your repository's foundation files.

## A real install

A real install serves a repository through a GitHub account the factory acts as, signs people in at a real provider, and is reached over HTTPS. A real GitHub App and real Google sign-in have been tested only against fakes so far ([README status](../../README.md#status)); verify each step on your install.

### 1. GitHub: an App and the token the factory acts as

The factory is built to act as a GitHub App installed on the repository (`cmd/factoryd/wiring/github_settings.go`):

- it accepts only webhook deliveries that name the installation `init --github-installation-id` gives (`pkg/github/webhook_receiver.go`), so a plain repository webhook, whose deliveries name no installation, is refused;
- it recognises its own pull requests and comments by the author login `init --github-app-login` gives, the App's bot login written as `name[bot]`.

Create the App with these repository permissions, which `sisf doctor` and the supervisor rely on (`internal/install/doctor_live.go`): Issues read and write (comments, labels, closing), Contents read and write (the supervisor pushes candidate branches `sisf/<factory>/<run>-<attempt>` and merges), Pull requests read and write (it opens and merges the candidate's pull request). Install it on the repository and note the installation id and the App's bot login.

**The token is an open product question.** The factory reads one GitHub token at `init`, from the environment variable `--github-token-env` names, stores it owner-only in `<dir>/secrets/control/github-token` and in the control and supervisor env files (`internal/install/init_env.go`), and sends it as a bearer token. Control and the supervisor read it once per start and never refresh it, so it must be long-lived. Nothing in this release mints, refreshes or rotates it: no code exchanges the App's key for installation tokens. An installation token of the App, which is what makes GitHub record the App as the author, expires after an hour; after that `sisf doctor` reports `the token was refused: it is wrong, expired or revoked` and GitHub calls fail. A token of another kind may live longer, but then GitHub records that token's account as the author, not the App's `name[bot]` login. Until the product decides this, a real install cannot run unattended for long; plan to replace the token (below) and verify each step on your install.

To replace the token: stop the factory, write the new token to `<dir>/secrets/control/github-token` and to the `SISF_GITHUB_TOKEN=` lines of `<dir>/secrets/control/runtime.env` and `<dir>/secrets/control/supervisor.env`, keeping the files owner-only, then `sisf up`.

A classic personal token should not carry the `workflow` scope: `sisf doctor` warns `the token carries the workflow scope, so code pushed with it can change CI workflows`.

### 2. GitHub: the App's webhook

GitHub delivers events to `/webhooks/github` on the control zone (`internal/control/routes.go`); the edge on 127.0.0.1:7460 routes the same path to control. Configure the App's webhook with:

- URL: `<external URL>/webhooks/github`, through the reverse proxy of step 5;
- secret: the content of `<dir>/secrets/control/webhook-secret`, which `init` generates (so create the install, step 4, before you finish this step);
- events: Issues, Issue comments and Pull requests (`pkg/github/webhook_normalize.go`).

A lost delivery is still picked up by reconciliation within one interval ([Reconciliation with GitHub](operations.md#reconciliation-with-github)).

### 3. Sign-in provider

Register the factory as a client at an OpenID Connect provider (or a GitHub OAuth app for GitHub sign-in) with the redirect URI `<external URL>/auth/callback/google` for OpenID Connect, `<external URL>/auth/callback/github` for GitHub (`cmd/factoryd/wiring/control_identity.go`). An Owner who decides approvals must sign in through OpenID Connect: GitHub sign-in cannot confirm a step-up ([security guide](security.md#limits-to-know)).

### 4. Create the install

```sh
export GITHUB_TOKEN=...      # the token of step 1, only in this shell
sisf init --dir /srv/sisf/factory --repo owner/app --github-token-env GITHUB_TOKEN \
  --github-installation-id 12345678 --github-app-login 'my-factory[bot]' \
  --external-url https://factory.example.com \
  --owner 'https://accounts.google.com#1234567890' \
  --oidc-issuer https://accounts.google.com --oidc-client-id <client id> \
  --oidc-client-secret-file /path/to/client-secret \
  --budget-cap 50 --alert-threshold 40
```

- GitHub sign-in instead of, or besides, OpenID Connect: `--github-login-client-id` with `--github-login-client-secret-file` or `--github-login-client-secret-env` (`--github-login-url` and `--github-login-api-url` for GitHub Enterprise).
- A client secret is read from the file or the variable the flag names, never from a flag value, is never printed, and lands only in the control zone's env file. Without a secret flag `init` accepts the client silently and control signs in without a client secret, so register a public client at the provider in that case.
- `--external-url` must be an `https` origin unless it is a loopback address. The fake identity provider (any loopback address on port 7468) is refused outside the simulation, by `init` and by `doctor`.
- `--owner` is repeatable; each is `issuer#subject` as the provider reports it.
- `--github-installation-id` and `--github-app-login` name the GitHub App installation and the account the factory writes as; `--github-app-id` optionally adds that account's numeric id. `init` writes them under `extensions:` in `<dir>/factory.yaml`, which control and the supervisor read at every start (`internal/install/github_app_options.go`, `cmd/factoryd/wiring/github_settings.go`). Without the first two, `init` refuses, writes nothing and exits 2 (observed): `a real install acts for a GitHub App and cannot start without it; pass --github-installation-id N ... and --github-app-login LOGIN ...`. Quote the login in the shell, since `[bot]` is a glob pattern.
- `--mode docker` makes a Docker install; the rest is the same.
- Every option is checked before anything is written; an invalid one exits 2 with the usage line, and a failed `init` leaves nothing behind. `init` prompts for the budget only when standard input is a terminal; otherwise, without a budget flag, it creates the install with the budget unconfirmed and says so.

### 5. HTTPS origin

Every port is bound to 127.0.0.1. Run a reverse proxy with TLS that forwards `<external URL>` to `127.0.0.1:7463` (control: dashboard, REST, MCP, sign-in and `/webhooks/github`). Browsers send the Fetch Metadata the dashboard's forms require only to an https or loopback origin, so a plain http origin off loopback refuses those forms ([Signing in](operations.md#signing-in)).

### 6. Start, check, approve the policy

1. `sisf up --dir /srv/sisf/factory` (Standalone stays in the foreground; Docker returns). It builds from the checkout `init` recorded. It prints `factory <id> is up: control http://127.0.0.1:7463, app via edge http://127.0.0.1:7460`.
2. `sisf doctor --dir /srv/sisf/factory`: every line `OK` or an understood `WARN`. `SKIP` lines appear only after a failed `running` check.
3. Commit the foundation files to the repository and [put the first policy in force](policy-and-approvals.md#putting-the-first-policy-in-force).
4. Sign in at the external URL as the Owner, and `sisf login --url https://factory.example.com` for the CLI.

Undo: `sisf down -v --dir /srv/sisf/factory` stops it if it runs and removes its data; then delete the directory, which holds the secrets and `factory.yaml`.

## Declare the inputs of the application

The factory publishes a candidate by itself only when every path it changes is an input of the one unit it releases; any other path is refused for the Owner's approval, and its pull request stays open for you ([threat model](../threat-model.md#merging-a-verified-candidate-tm-02-tm-08), `internal/supervisor/release/unit_inputs.go`). Built in, the inputs of `app` are the packages of `examples/hello-app/`, without any `go.mod`, `go.sum` or `go.work*` file; its Dockerfile is not an input. `control` has no built-in inputs: the dashboard's templates and scripts show the Owner what an approval is for, so they are part of the trusted surface, and a change to the dashboard is published only by a person. An application not laid out like `examples/hello-app/` must declare its inputs, otherwise every one of its candidates is refused for approval. Add `release.unit_inputs` to the install's `<dir>/factory.yaml`, the manifest the supervisor reads, and restart the factory, since the supervisor reads it when it starts:

```yaml
release:
  units: [app]
  unit_inputs:
    app: ["*.go"]
```

- A pattern is a path relative to the repository root, matched against the whole path without regard to letter case; `*` stays within one directory, so `*.go` is the Go files at the root and `web/*.html` the pages directly in `web/`. A name without a wildcard is that one file. An entry replaces the built-in inputs of that unit. A unit needs an entry in `release.units` to have one.
- `**`, an absolute path, `..` and a pattern made only of wildcards are refused (`pkg/manifest/unit_inputs.go`).
- `sisf conformance check <dir>/factory.yaml` and `sisf up` refuse a pattern that can match a protected path (`release.unit_inputs_protected`), which includes `ui/templates/` and `ui/static/`, a pattern of any unit but `app` that can match a file the Go toolchain reads, `go.mod`, `go.sum` and `go.work*` included (`release.unit_inputs_go_code`), and a pattern that can match a path another unit consumes (`release.unit_inputs_overlap`).
- A dashboard pattern, if you declare one for `control`, must stay outside `ui/`. Never declare a template or script that shows an approval or a step-up sign-in: a candidate could then change what you are asked to approve.
- The supervisor never reads inputs from the repository, so a candidate cannot widen its own. The installed file is Owner-declared and protected by the install directory's permissions (a read-only mount in Docker mode); it is not signed.

## Running Standalone as a service

`init` writes a systemd user unit and a launchd agent to `<dir>/service/`, each with copy-and-enable instructions in its header (`templates/standalone/`). Each runs `sisf up --mode standalone --dir <dir> --source <checkout>` with the checkout as its working directory and a `PATH` holding the directories where `init` found `go`, `git`, `gh`, `claude` and `codex`, then the system's (`internal/install/service_units.go`). The launchd agent writes `<dir>/logs/service.log`.

The units run the `sisf` program that wrote them, so write them with one kept at a stable path. `init` skips them with a warning when it runs from a temporary `go run` build or when `go` is not on `PATH`. To write them later, or again after `sisf` or the checkout moved, run `sisf service units --dir <dir>` (and `--source <checkout>` for a moved checkout); nothing else in the install changes.

## Real agents in Standalone

A Standalone install may run claude or codex as local processes under `trusted_code_only`:

```sh
sisf init --dir /srv/sisf/factory --repo owner/app --github-token-env GITHUB_TOKEN ... \
  --coder-agent claude --coder-model <model> --agent-home "$HOME" \
  --qa-judge agent --qa-agent codex --qa-model <model> --qa-home "$HOME" \
  --exec local --build-command 'go build ./...'
```

- `--agent-home` and `--qa-home` let an agent use an existing login, such as the one in your own home. They are refused when the directory is, holds or lies inside the factory's secrets, state or trust directories, and the installer refuses an install directory inside that home (or that home inside it). The default install directory `~/.sisf/factory` lies inside your home, so pass `--dir` elsewhere.
- `--coder-model` and `--qa-model` replace the tier names of the policy's model routes, which the real tools do not accept.
- With `--exec local` candidate builds are held: the supervisor builds code an agent wrote only in a sandbox of an isolating backend, and the local backend is not one (`cmd/supervisor/stages/execution_policy.go`). Outside the simulation, a Standalone supervisor then refuses to start its lifecycle stages with `PERMISSION_DENIED` rather than run an agent's code on the host, and `sisf up` reports it. To build, judge and release, give the install a Docker build sandbox (below).

### Building in a Docker sandbox

The agents keep running as local processes under `trusted_code_only`; only the builds move into Docker, and the QA agent, which reads those untrusted candidates, then runs through a sandboxing launcher, as the supervisor requires under `--exec docker` (`cmd/supervisor/qa_judge_wiring.go`). You provide the sandbox:

- an internal Docker network for the runners (`docker network create --internal NET`);
- an egress proxy on that network as its only way out, for example the edge's (`edge --egress-listen=egress:3128 --allow-non-loopback ...` in a container attached to `NET` with the alias `egress` and to an outbound network);
- a runner image that holds the build command. The supervisor runs the command once per phase, each time in a fresh container, with the phase in `SISF_PHASE`: `test` (only the exit status is read; 0 means the tests passed), `build` (anything but exit 0 refuses the candidate) and `report`, which runs no code of the candidate. The report phase is told the candidate as `SISF_REPORT_COMMIT`, `SISF_REPORT_UNIT` and `SISF_REPORT_BRANCH`, and a nonce in `SISF_REPORT_NONCE`; it prints `sisf-report <nonce> <key>: <value>` lines for `image-digest`, `image`, `sbom` and `provenance`, each exactly once (`cmd/supervisor/stages/image_builders.go`; `tests/smoke/sandbox/build-candidate.sh` is an example). A build whose report names no SBOM and provenance is held before QA, and one whose tests did not pass fails the tests gate. Pin it by content, the way the docker backend runs it (`adapters/exec/docker/sandbox.go`): `name@sha256:<digest>` by a registry digest, or the full ID of an image built on this host, `sha256:<64 hex>` (what `docker image inspect --format '{{.Id}}'` prints, and how Docker mode pins the runner it builds itself). `init --runner-image` refuses a tag and a short ID (`internal/install/standalone_build_sandbox.go`);
- the runner Docker request filter: `sisf docker-filter --role runner --listen 127.0.0.1:PORT --factory-id ID --network NET --image IMAGE --socket SOCKET`, started by you before `sisf up` (in Docker mode the Compose project starts both filter roles itself; `sisf help docker-filter` says which use is which);
- a launcher that runs the QA agent in a sandbox and keeps its standard input and output.

```sh
sisf init --dir /srv/sisf/factory --repo owner/app ... \
  --coder-agent claude --coder-model <model> --agent-home "$HOME" \
  --qa-judge agent --qa-agent codex --qa-model <model> --qa-agent-binary /srv/sisf/qa-launcher \
  --exec docker --exec-docker-endpoint http://127.0.0.1:PORT --exec-docker-network NET \
  --egress-proxy http://egress:3128 --runner-image registry/runner@sha256:... --build-command /usr/local/bin/build
```

- `init` refuses a partial sandbox, a filter off loopback, an image not pinned by content (`name@sha256:<digest>` or `sha256:<64 hex>`), a sandbox without `--build-command` or `--qa-agent-binary`, and `--qa-home` with it: the launcher owns the QA agent's home (`internal/install/standalone_build_sandbox.go`).
- The supervisor reads `NET` through the runner filter (`GET /networks/NET`, the one network read the runner filter allows) and builds nothing unless Docker reports it internal: on an ordinary bridge a runner would reach the internet around the egress proxy (`adapters/exec/docker/backend.go`). `sisf doctor` fails the `sandbox network` check for the same reason, in Docker mode for the Compose project's `-sandbox` network as well.
- A build holds no secret, so the build command fetches the candidate's commit (`SISF_COMMIT`) without one: through the egress proxy from a public repository, or from a mirror you serve on the sandbox network.
- Standalone's local deployment backend runs the candidate's own program. The supervisor tells the build command's report phase its operating system and architecture (`SISF_REPORT_TARGET_OS`, `SISF_REPORT_TARGET_ARCH`); the command must export the unit's one program for it on the report's lines (`sisf-report <nonce> program: <base64>`), and the reported `image-digest` must be the sha256 of exactly those bytes. A build that exports nothing, or another program than the digest names, is held as an outage (`cmd/supervisor/stages/report_reader.go`; `tests/smoke/sandbox/build-candidate.sh` is a working command).
- The supervisor keeps exported programs under `data/trust/programs/`, hashes a program again before every start, and removes those that are neither the last known good release nor in flight. A release whose digest it has no program for is not started; the installed program runs only under the digest `sisf up` pinned for it. The promoted program is started again after a restart and after `sisf down` and `sisf up`; if its file is gone or changed, `sisf up` says so and the application starts from the installed release.
- A candidate that passed QA therefore runs as a host process with the rights of the application unit, your operating-system user: the build is sandboxed, the released program is not ([ADR 0022](../adr/0022-standalone-deploys-the-candidates-own-program.md)).
- The local backend moves all traffic to the new program in one step; the release is then watched and, on a failure, rolled back to the last known good release. It is not a canary rollout with a traffic share.
- The supervisor records the application process of each slot under `data/trust/local-slots/`. At start the deploy backend finds the processes an earlier supervisor left again, verified by pid and start time, and serves from them; it stops them when a release is staged over their slot and when the supervisor stops (`adapters/deploy/local/slot_records.go`, [operations](operations.md#a-release-that-was-not-watched-to-its-end)).
- The smoke run (`tests/smoke`) provisions all of this for itself, with a git mirror of the private sandbox repository and a launcher that runs codex in a container (`tests/smoke/sandbox/`).
