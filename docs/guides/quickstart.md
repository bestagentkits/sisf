# Quickstart

Run one factory locally against a simulated GitHub. The simulation profile needs no GitHub token and no real identity provider. Real GitHub App and Google SSO flows are not covered here; they are tested only against fakes so far.

The flags below are owned by the CLI: run `sisf help <command>` (or `go run ./cmd/sisf help <command>` from a checkout) for the current list.

## Prerequisites

- A checkout of this repository, built from source (Go 1.25, see [ADR 0019](../adr/0019-go-1-25-toolchain.md)). `sisf up` builds the factory from the current directory unless `--source` says otherwise.
- Docker mode only: Docker with Compose.

The factory uses ports 7460-7469 on 127.0.0.1 and nothing else.

## Standalone mode

Standalone is the trusted-code profile. It runs the units as local processes and stays in the foreground.

```sh
go run ./cmd/sisf init --repo acme/hello --profile sim --dir ./factory
go run ./cmd/sisf up --dir ./factory --confirm-unlimited --no-alert
```

In another terminal:

```sh
go run ./cmd/sisf doctor --dir ./factory
go run ./cmd/sisf down --dir ./factory
```

`up` also accepts the `init` flags and creates the install when the directory holds none, so `init` is optional.

## Docker Compose mode

```sh
go run ./cmd/sisf init --repo acme/hello --profile sim --mode docker --dir ./factory
go run ./cmd/sisf up --dir ./factory --confirm-unlimited --no-alert
go run ./cmd/sisf doctor --dir ./factory
go run ./cmd/sisf down --dir ./factory
```

`up` starts one Compose project and returns. `doctor` warns that container sandboxing is not available in this release; do not run untrusted code on it.

## What the commands do

| Command | Behaviour is owned by |
|---|---|
| `sisf init` | `cmd/sisf/cmd_install.go`, `internal/install` |
| `sisf up` | same; builds, signs the release manifest, checks isolation, prepares the state store, starts every unit |
| `sisf doctor` | `cmd/sisf/cmd_doctor.go`; prints ok, warn or fail per check and exits 1 on any failure |
| `sisf down` | stops the factory; `-v` also removes data; secrets and `factory.yaml` are never removed |

## Notes

- The default budget mode is unlimited and must be confirmed (`--confirm-unlimited` with `--alert-threshold AMOUNT` or `--no-alert`). Until confirmed, the factory takes in issues and starts no work ([ADR 0007](../adr/0007-budget-default-unlimited.md)).
- Secrets are written owner-only under `<dir>/secrets` and never printed. The control server no longer accepts an Owner token: sign in to the dashboard at `http://127.0.0.1:7463` instead. In the simulation profile that sign-in is the fake identity provider of `sisf sim oidc` on `127.0.0.1:7468`, which must be running; see [Signing in](operations.md#signing-in).
- `--dir` defaults to `$SISF_HOME`, else `~/.sisf/factory`.
- Day-to-day use is in the [operations guide](operations.md); emergencies are in the [incident runbook](incident.md).
