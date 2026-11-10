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
- Candidate builds are held in this release: the supervisor builds code an agent wrote only in a sandbox of an isolating backend, and the local backend is not one (`cmd/supervisor/stages/execution_policy.go`). Outside the simulation, a Standalone supervisor therefore refuses to start its lifecycle stages with `PERMISSION_DENIED` rather than run an agent's code on the host, and `sisf up` reports it. Nothing is built, judged or released until an isolating backend exists for Standalone.

## Development install

`--dev` is a Standalone install on loopback that signs in at the fake identity provider, for one person on their own machine:

```sh
sisf init --dev --mode standalone --dir /srv/sisf/dev --repo owner/app --github-credential gh-cli --budget-cap 5
```

- `--github-credential gh-cli` takes the GitHub token from `gh auth token` at `init` and at every `up`; it is accepted only with `--dev`.
- `--dev` is refused in Docker mode, with `--profile sim`, with a sign-in provider and with an external URL off loopback. `init` and `doctor` print a loud warning: anyone who can reach 127.0.0.1 on that machine signs in as the Owner.

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
