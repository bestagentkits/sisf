# Quickstart

Run one factory locally against a simulated GitHub. The simulation profile needs no GitHub token and no real identity provider: it signs people in at a fake identity provider the install runs itself. Real GitHub App and Google SSO flows are not covered here; they are tested only against fakes so far.

The flags below are owned by the CLI: run `sisf help <command>` (or `go run ./cmd/sisf help <command>` from a checkout) for the current list.

## Prerequisites

- A checkout of this repository, built from source (Go 1.25, see [ADR 0019](../adr/0019-go-1-25-toolchain.md)). `sisf up` builds the factory from the current directory unless `--source` says otherwise.
- Docker mode only: Docker with Compose.

The factory uses ports 7460-7469 on 127.0.0.1 and nothing else.

## Standalone mode

Standalone is the trusted-code profile. It runs the units as local processes and stays in the foreground.

```sh
go run ./cmd/sisf init --repo acme/hello --profile sim --dir ./factory --confirm-unlimited --no-alert
go run ./cmd/sisf up --dir ./factory
```

In another terminal:

```sh
go run ./cmd/sisf doctor --dir ./factory
go run ./cmd/sisf down --dir ./factory
```

`up` also accepts the `init` flags and creates the install when the directory holds none, so `init` is optional.

## Docker Compose mode

```sh
go run ./cmd/sisf init --repo acme/hello --profile sim --mode docker --dir ./factory --confirm-unlimited --no-alert
go run ./cmd/sisf up --dir ./factory
go run ./cmd/sisf doctor --dir ./factory
go run ./cmd/sisf down -v --dir ./factory
```

`up` starts one Compose project and returns. On a fresh simulation install `doctor` reports 0 failures and one warning: the artifacts image is not built until a release builds it.

## Signing in

Open `http://127.0.0.1:7463`. The simulation signs you in as the Owner (`http://127.0.0.1:7468#owner`) at the fake identity provider on `127.0.0.1:7468`, which the install starts beside control: a Standalone unit, or the `sim-oidc` service in control's network namespace in Docker mode. Nothing to click. The CLI signs in the same way:

```sh
go run ./cmd/sisf login --dir ./factory
```

opens a device login: visit the printed URL, check the code and approve. There is no Owner token; see [Signing in](operations.md#signing-in).

## A real install

Outside the simulation, `sisf init` refuses to create a factory nobody can sign in to. Name the origin people reach control at, the Owner, and at least one provider:

```sh
sisf init --repo owner/app --github-token-env GITHUB_TOKEN \
  --external-url https://factory.example.com \
  --owner 'https://accounts.google.com#1234567890' \
  --oidc-issuer https://accounts.google.com --oidc-client-id <client id> \
  --oidc-client-secret-file /path/to/client-secret \
  --budget-cap 50 --alert-threshold 40
```

- GitHub sign-in instead of, or besides, OpenID Connect: `--github-login-client-id` with `--github-login-client-secret-file` or `--github-login-client-secret-env` (`--github-login-url` and `--github-login-api-url` for GitHub Enterprise).
- A client secret is read from the file or the variable the flag names, never from a flag value, is never printed, and lands only in the control zone's env file.
- `--external-url` must be an `https` origin unless it is a loopback address. The fake identity provider (any loopback address on port 7468) is refused outside the simulation, by `init` and by `doctor`.
- `--owner` is repeatable; each is `issuer#subject` as the provider reports it.

## Budget

Every install must settle the budget before the factory claims work ([ADR 0007](../adr/0007-budget-default-unlimited.md)):

- unlimited: `--confirm-unlimited` with `--alert-threshold AMOUNT` or `--no-alert`;
- capped: `--budget-cap AMOUNT`, optionally `--alert-threshold AMOUNT` below it and `--run-estimate AMOUNT`, what each run sets aside before it is claimed (default a fifth of the cap). The dispatcher gets it as `--run-estimate-micros`.

Amounts are whole or decimal currency units. Until the budget is confirmed, the factory takes in issues and starts no work.

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
- a runner image that holds the build command and reports `sisf-image-digest:` and `sisf-image:` lines (`cmd/supervisor/stages/image_builders.go`), pinned as `name@sha256:...` by a registry digest: the docker backend runs no image by tag or local image ID, so push a locally built image to a registry first, such as one on loopback;
- the runner Docker request filter: `sisf docker-filter --role runner --listen 127.0.0.1:PORT --factory-id ID --network NET --image IMAGE --socket SOCKET`, started before `sisf up`;
- a launcher that runs the QA agent in a sandbox and keeps its standard input and output.

```sh
sisf init --dir /srv/sisf/factory --repo owner/app ... \
  --coder-agent claude --coder-model <model> --agent-home "$HOME" \
  --qa-judge agent --qa-agent codex --qa-model <model> --qa-agent-binary /srv/sisf/qa-launcher \
  --exec docker --exec-docker-endpoint http://127.0.0.1:PORT --exec-docker-network NET \
  --egress-proxy http://egress:3128 --runner-image registry/runner@sha256:... --build-command /usr/local/bin/build
```

- `init` refuses a partial sandbox, a filter off loopback, an image not pinned by digest, a sandbox without `--build-command` or `--qa-agent-binary`, and `--qa-home` with it: the launcher owns the QA agent's home (`internal/install/standalone_build_sandbox.go`).
- The supervisor reads `NET` through the runner filter (`GET /networks/NET`, the one network read the runner filter allows) and builds nothing unless Docker reports it internal: on an ordinary bridge a runner would reach the internet around the egress proxy (`adapters/exec/docker/backend.go`). `sisf doctor` fails the `sandbox network` check for the same reason, in Docker mode for the Compose project's `-sandbox` network as well.
- A build holds no secret, so the build command fetches the candidate's commit (`SISF_COMMIT`) without one: through the egress proxy from a public repository, or from a mirror you serve on the sandbox network.
- Standalone's local deployment backend starts the installed application for every release; the sandboxed build is tested and digested, not deployed.
- The smoke run (`tests/smoke`) provisions all of this for itself, with a git mirror of the private sandbox repository and a launcher that runs codex in a container (`tests/smoke/sandbox/`).

## Development install

`--dev` is a Standalone install on loopback that signs in at the fake identity provider, for one person on their own machine:

```sh
sisf init --dev --mode standalone --dir /srv/sisf/dev --repo owner/app --github-credential gh-cli --budget-cap 5
```

- `--github-credential gh-cli` takes the GitHub token from `gh auth token` at `init` and at every `up`; it is accepted only with `--dev`.
- Such an install has no GitHub App installation: the factory acts as your gh login, takes no webhooks (`/webhooks/github` is not served) and learns of issues only through the control zone's reconciliation with GitHub (`cmd/factoryd/wiring/control_reconcile.go`; default every minute; `sisf init --github-reconcile-interval 15s` sets it, 10s to 1h). Control starts only if GitHub confirms the token may write to the repository, and that access stands in for the installation in role checks (`cmd/factoryd/wiring/control_operator_token_access.go`). Every other non-simulation install must name its installation in `extensions.github_installation_id` and its account in `extensions.github_app_login`; `sisf doctor` fails the `github events` check otherwise.
- Sign-in is locked, because real agents run as local processes beside the fake identity provider and can reach every loopback port. Each `sisf up` writes a new secret to `<dir>/secrets/control/dev-sign-in` (owner-only, never in a unit's environment, command line or log) and prints where it is; open `http://127.0.0.1:7468/_sim/unlock` and enter it once per browser, then sign in at `http://127.0.0.1:7463`. Without it the fake refuses every sign-in and every `/_sim` endpoint (`cmd/sisf/cmd_sim_oidc_sign_in_gate.go`). The browser keeps an HMAC of the secret in an HttpOnly, `SameSite=Strict` cookie, never the secret, and the fake takes a POST to `/_sim` only with an `Origin` naming its own origin, so a page served on another loopback port cannot drive it through your browser. Use `127.0.0.1` for both the factory and the fake, as printed: the Strict cookie is not sent when a sign-in starts from another site such as `localhost`. The simulation profile stays unlocked.
- `--dev` is refused in Docker mode, with `--profile sim`, with a sign-in provider and with an external URL off loopback. `init` and `doctor` print a loud warning: anyone who can read the sign-in secret, which includes an agent running as your user under `trusted_code_only`, signs in as the Owner.

## What the commands do

| Command | Behaviour is owned by |
|---|---|
| `sisf init` | `cmd/sisf/cmd_install*.go`, `internal/install` |
| `sisf up` | same; builds, signs the release manifest, checks isolation, prepares the state store, starts every unit |
| `sisf doctor` | `cmd/sisf/cmd_doctor.go`; prints ok, warn or fail per check and exits 1 on any failure |
| `sisf down` | stops the factory; `-v` also removes data; secrets and `factory.yaml` are never removed |

## Notes

- Secrets are written owner-only under `<dir>/secrets` and never printed.
- `--dir` defaults to `$SISF_HOME`, else `~/.sisf/factory`.
- Day-to-day use is in the [operations guide](operations.md); emergencies are in the [incident runbook](incident.md).
