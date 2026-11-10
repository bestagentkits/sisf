# Operations

How to run and inspect an installed factory. Behaviour is owned by the code; this page says where to look. Flags: `sisf help <command>`.

## Lifecycle

| Task | Command |
|---|---|
| Start (builds, signs, checks isolation, starts all zones) | `sisf up` |
| Stop and keep everything | `sisf stop` |
| Restart every unit | `sisf restart` (Standalone also restarts on SIGHUP) |
| Stop and remove what the run created | `sisf down` (`-v` removes data too) |
| Health of the install | `sisf doctor` |

All of these take `--dir`; the default is `$SISF_HOME`, else `~/.sisf/factory`. Run `sisf doctor` after every start and before relying on rollback: it checks that a last known good release is recorded.

## Inspecting the factory

- `sisf status` shows the overview. `sisf issues` lists tracked issues.
- `sisf runs inspect <issue|run-id>` shows the record; `sisf runs explain <issue|run-id>` says why a run is stopped or blocked.
- `sisf approvals list`, `sisf approvals get <id>` and `sisf approvals decide <id>` handle Owner approvals. A decision is bound to the approval's nonce, subject digest and policy revision.
- `sisf call <action>` calls any action over REST (the catalogue is in [`api/openapi.yaml`](../../api/openapi.yaml)). Add `--dry-run` to preview.

Every surface runs through one action service (`internal/actions`), so none holds extra power. Parity across all five surfaces is still being integrated; see the [acceptance matrix](../acceptance-matrix.md).

## Signing in

`sisf login` starts a device-style login: open the printed URL, check the code and approve. The CLI stores a short-lived token owner-only. The control URL comes from `--url`, then `$SISF_URL`, then `http://127.0.0.1:7463`; `$SISF_TOKEN` overrides the stored login.

## State and budget

`sisf state bootstrap` prepares the state store (migrations, one role per zone, the budget record). `sisf up` does this for you. The budget mode default and its confirmation are in [ADR 0007](../adr/0007-budget-default-unlimited.md); code is in `internal/budget`.

## Ports

Host ports 7460-7469 on 127.0.0.1; the owner is `internal/install/options.go`.

## When something is wrong

Use the [incident runbook](incident.md).
