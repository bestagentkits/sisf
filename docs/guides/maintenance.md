# Maintenance: upgrade, back up, restore

How to move a factory to a newer build, and how to copy and restore what it holds. The product has no upgrade, backup or restore command yet; these are the manual procedures that work with what exists. The decisions they will follow are still proposals: [ADR 0012](../adr/0012-backup-and-restore.md) (backup targets) and [ADR 0013](../adr/0013-schema-migration-on-self-update.md) (migrations compatible across two versions) await the Owner, so nothing guarantees today that a newer build's schema can be read by an older one.

## What an install holds

Everything of a Standalone install lives below its install directory `<dir>` (`internal/install/layout.go`):

| Path | What | Lost if missing |
|---|---|---|
| `factory.yaml` | the install's manifest, with pinned digests | the install |
| `budget-confirmation.json` | the Owner's budget confirmation | the budget confirmation |
| `source.json` | the source checkout `sisf up` and the service units build from | the recorded checkout; pass `--source` |
| `secrets/` | per-zone credentials, the root signing key, the GitHub token, the webhook secret, sign-in client secrets | every credential; the root key cannot be recreated |
| `release/release-manifest.json` | the signed release manifest | rebuilt by `sisf up` |
| `data/state/factory.db` (and its `-wal`, `-shm` files) | the operational state: issues, runs, approvals, the policy record, the ledger, the audit log | the factory's memory |
| `data/control/` | the control zone's encrypted secret store and its key (`local:` references) | `local:` secrets |
| `data/trust/` | the supervisor's delegated keys and last known good releases | rollback targets |
| `data/app/`, `data/artifacts/`, `data/workspaces/`, `data/qa-workspaces/` | application data, build artifacts, agent workspaces | application data |
| `bin/`, `logs/`, `run/`, `service/` | built programs, logs, the pid file, unit files | rebuilt or rewritten |

A Docker install keeps `factory.yaml`, `budget-confirmation.json`, `source.json` (written by `init` in every mode), `secrets/`, `release/` and the rendered `compose.yaml` in `<dir>`, and its data in named volumes, each `<project>-<name>` where the project is `sisf-<repository name>` unless `--factory-id` said otherwise (`templates/compose/compose.yaml`): `state-data` (PostgreSQL), `control-data`, `trust`, `app-data`, `artifacts`, `workspaces`, `qa-workspaces`, and `supervisor-admin` (a socket only).

The env files hold absolute paths (Standalone: the SQLite path; both modes: unit files), so restore an install to the same directory it was made in.

## Back up

The commands below use `D` for the install directory; set it first, for example `D=$HOME/.sisf/factory`.

Prerequisites: the factory is stopped, so no process writes the database while it is copied.

1. Stop it: `sisf stop --dir "$D"`. In Standalone the command returns once the foreground `sisf up` has exited; `sisf up` running elsewhere (a terminal, a service) ends with it. A service manager set to restart it must be stopped instead (`systemctl --user stop ...`, `launchctl bootout ...`).
2. Archive the whole directory, keeping permissions:

   ```sh
   tar -C "$(dirname "$D")" -cpf sisf-backup.tar "$(basename "$D")"
   ```

3. Docker mode only: the data is in the project's named volumes, which step 2 does not include. Copy each with your usual Docker volume tool while the project is stopped. One way, with standard Docker (not exercised by this project's tests), for the project `sisf-hello`:

   ```sh
   for v in state-data control-data trust app-data artifacts workspaces qa-workspaces; do
     docker run --rm -v sisf-hello-$v:/from:ro -v "$PWD":/to alpine tar -C /from -cpf /to/$v.tar .
   done
   ```

4. Start it again: `sisf up --dir "$D"`, then `sisf doctor --dir "$D"`.

The archive holds the root signing key and every credential in clear text: keep it where only the Owner can read it.

Verify: `tar -tf sisf-backup.tar` lists `factory.yaml`, `secrets/control/root-key.pem` and, in Standalone, `data/state/factory.db`.

## Restore

Standalone (observed on a scratch install: after the restore the policy record was back in force):

1. Stop the factory (as above) and move the current directory aside: `mv "$D" "$D.old"`.
2. Unpack the archive to the same path: `tar -C "$(dirname "$D")" -xpf sisf-backup.tar`.
3. With the checkout recorded in `$D/source.json` at the same build that made the backup (or another checkout of it named with `--source`): `sisf up --dir "$D"`, then `sisf doctor --dir "$D"`.

Docker mode: unpack `<dir>` the same way. Putting the volumes back has no tested procedure in this release: each volume must be refilled under the same name while no container uses it, and nothing here has been exercised.

What is not restored: the state of everything after the backup, and the trust record, which `sisf up` replaces with the release it builds; GitHub keeps issues, comments and pull requests made since, and the factory meets them again through reconciliation. Sessions and tokens are in memory and lost on every restart anyway, so everyone signs in again.

Undo: stop the factory, `rm -rf "$D"`, and `mv "$D.old" "$D"`.

## Upgrade

`sisf up` builds every program or image from the source checkout each time it starts, migrates the state store forward, signs a new release manifest and installs that release as the last known good one (`internal/install/release.go`, `internal/install/trust_material.go`). Upgrading is therefore: new source, then `sisf up`. Every `sisf up` replaces the trust record this way, so the releases promoted during an earlier run are forgotten: afterwards a rollback can return only to the release `sisf up` just installed.

1. [Back up](#back-up) (the factory is stopped after step 1 of it).
2. Update the checkout the install recorded in `$D/source.json` to the build you want, and rebuild the command line: `git pull` (or `git checkout <tag or commit>`), then `make install`. `sisf version` names the new build.
3. `sisf up --dir "$D"`. Expected: `building ...` lines, then `factory <id> is up: control http://127.0.0.1:7463, app via edge http://127.0.0.1:7460`.
4. `sisf doctor --dir "$D"`.

Verify: `sisf doctor` reports no failure, its `release manifest` line is `OK`, and `sisf status` answers.

Undo: there are no down migrations. Stop the factory, [restore](#restore) the backup of step 1, check out the previous commit, `make install`, and `sisf up`.

The rebuilt programs get new digests in `<dir>/factory.yaml`. If the repository's `.sisf/factory.yaml` is meant to match the install's file, committing the new one is a [policy change](policy-and-approvals.md#changing-the-policy).

## Renewing the supervisor's delegated keys

`sisf up` issues the supervisor's delegated signing keys from the root key. `sisf doctor` warns `delegated keys  they expire at <time>; run sisf up to issue new ones` as they near expiry and fails once one has expired. Stop the factory and run `sisf up` again.

## Removing an install

`sisf down -v --dir "$D"` stops the factory and removes its data: `<dir>/data` in Standalone, and in Docker mode the project's containers, networks and volumes. It does so whether or not the factory runs. Secrets and `factory.yaml` are never removed by it; delete the directory yourself afterwards. Note that `-v` also removes the control zone's `local:` secret store and the trust record.
