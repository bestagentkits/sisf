# Maintenance: upgrade, back up, restore

How to move a factory to a newer build, and how to copy and restore what it holds. The product has no upgrade, backup or restore command yet; these are the manual procedures that work with what exists. [ADR 0012](../adr/0012-backup-and-restore.md) (backup targets) is still a proposal; [ADR 0013](../adr/0013-schema-migration-on-self-update.md) (migrations compatible across two versions) is accepted for what is described here. What the code does today: each migration is marked backward compatible or not, and a build works on a schema ahead of it only when every migration it does not know is marked compatible; otherwise its store refuses to start (`pkg/state/sqlstore/migrate.go`). A migration that is not compatible needs the Owner's approval on a store that has migrations applied, given with `sisf up --approve-migrations` after a snapshot of the store ([an upgrade with a migration that is not backward compatible](#an-upgrade-with-a-migration-that-is-not-backward-compatible)); a database that holds no store yet takes every migration. Migration 12 is the one migration this release ships that is not compatible (`pkg/state/sqlstore/migrations.go`).

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
| `data/trust/` | the supervisor's delegated keys, last known good releases, the programs of released candidates (`programs/`) and the record of each slot's application process (`local-slots/`) | rollback targets |
| `data/app/`, `data/artifacts/`, `data/workspaces/`, `data/qa-workspaces/` | application data, build artifacts, agent workspaces | application data |
| `bin/`, `logs/`, `run/`, `service/` | built programs, logs, the pid file, the run lock `run/sisf.lock` and the record of each running unit under `run/units/`, unit files | rebuilt or rewritten; the run lock file is never deleted, and the install directory must be on a file system that supports `flock` |

A Docker install keeps `factory.yaml`, `budget-confirmation.json`, `source.json` (written by `init` in every mode), `secrets/`, `release/` and the rendered `compose.yaml` in `<dir>`, and its data in named volumes, each `<project>-<name>` where the project is `sisf-<repository name>` unless `--factory-id` said otherwise (`templates/compose/compose.yaml`): `state-data` (PostgreSQL), `control-data`, `trust`, `app-data`, `artifacts`, `workspaces`, `qa-workspaces`, and `supervisor-admin` (a socket only).

The env files hold absolute paths (Standalone: the SQLite path; both modes: unit files), so restore an install to the same directory it was made in.

## Back up

The commands below use `D` for the install directory; set it first, for example `D=$HOME/.sisf/factory`.

Prerequisites: the factory is stopped, so no process writes the database while it is copied.

1. Stop it: `sisf stop --dir "$D"`. In Standalone the command returns once the foreground `sisf up` has exited and no unit it started lives; `sisf up` running elsewhere (a terminal, a service) ends with it. If that `sisf up` was killed, its units still hold the install's run lock, and `sisf stop` stops them itself. A service manager set to restart it must be stopped instead (`systemctl --user stop ...`, `launchctl bootout ...`).
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

What is not restored: the state of everything after the backup, and the trust record, which `sisf up` replaces with the release it builds (a Standalone install keeps a promoted release whose program is still stored, as in [upgrade](#upgrade)); GitHub keeps issues, comments and pull requests made since, and the factory meets them again through reconciliation. Sessions and tokens are in memory and lost on every restart anyway, so everyone signs in again.

Undo: stop the factory, `rm -rf "$D"`, and `mv "$D.old" "$D"`.

## Upgrade

`sisf up` builds every program or image from the source checkout each time it starts, migrates the state store forward, signs a new release manifest and installs that release as the last known good one (`internal/install/release.go`, `internal/install/trust_material.go`). Upgrading is therefore: new source, then `sisf up`. In Docker mode and in the simulation, every `sisf up` replaces the trust record this way, so the releases promoted during an earlier run are forgotten: afterwards a rollback can return only to the release `sisf up` just installed. A Standalone install keeps a promoted release when the supervisor's program store still holds its program unchanged, and otherwise says that the application starts from the installed release again (`internal/install/standalone_promoted_program.go`).

The supervisor records the application process of each slot under `data/trust/local-slots/`. At start the deploy backend finds the processes an earlier supervisor left again, verified by pid and start time, and serves from them; it stops them when a release is staged over their slot and when the supervisor stops ([operations](operations.md#a-release-that-was-not-watched-to-its-end)).

1. [Back up](#back-up) (the factory is stopped after step 1 of it).
2. Update the checkout the install recorded in `$D/source.json` to the build you want, and rebuild the command line: `git pull` (or `git checkout <tag or commit>`), then `make install`. `sisf version` names the new build.
3. For a Standalone install run as a service, write its units again with `sisf service units --dir "$D"` and load them again ([service units](install.md#running-standalone-as-a-service)). Units written by an older `sisf` restart `sisf up` after a refusal too: launchd keeps the agent alive whatever the exit status and systemd lacks `RestartPreventExitStatus`, so an up that can never start is refused again every few seconds.
4. `sisf up --dir "$D"`. Expected: `building ...` lines, then `factory <id> is up: control http://127.0.0.1:7463, app via edge http://127.0.0.1:7460`.
5. `sisf doctor --dir "$D"`.

Verify: `sisf doctor` reports no failure, its `release manifest` line is `OK`, and `sisf status` answers.

Migrations 7 to 11 and 13 are backward compatible; migration 12 is not, and all pending migrations apply in one transaction or none do. Migration 12 takes UPDATE on the reservations from the agent zone's PostgreSQL role, which the previous dispatcher needs to claim an issue, so on a store that predates it step 4 fails with `APPROVAL_REQUIRED: a pending migration is not backward compatible and needs the Owner's approval: run sisf up --approve-migrations=12, ...` and applies nothing; follow [an upgrade with a migration that is not backward compatible](#an-upgrade-with-a-migration-that-is-not-backward-compatible). A Standalone factory started by an older `sisf` holds no run lock: the new `sisf` refuses to start beside it, names its process and asks you to stop it first ([troubleshooting](troubleshooting.md#installing-and-starting)). A factory started by an older `sisf` names no root key in `/healthz`, so the CLI refuses to send it a token until it is restarted with `sisf up`.

Undo: there are no down migrations. Stop the factory, [restore](#restore) the backup of step 1, check out the previous commit, `make install`, and `sisf up`.

The rebuilt programs get new digests in `<dir>/factory.yaml`. If the repository's `.sisf/factory.yaml` is meant to match the install's file, committing the new one is a [policy change](policy-and-approvals.md#changing-the-policy).

### An upgrade with a migration that is not backward compatible

Such a migration takes something the previous build needs, so after it the previous build refuses to start on the store (`the state store schema has migration <n>, which this release does not know and which is not backward compatible`) and going back means restoring the store as well as the build. `sisf up` therefore applies it only on the Owner's approval, naming exactly the versions the refusal named, and snapshots the store before migrating (ADR 0013, `internal/install/state_approved_migration.go`).

1. Do steps 1 to 3 of the [upgrade](#upgrade). The refusal of step 4 names the versions: `run sisf up --approve-migrations=12`.
2. `sisf up --dir "$D" --approve-migrations=12`. Expected, Docker mode: `dumping the state store to <dir>/backups/state-before-migration-12-<time>.dump before the approved migration`, then the usual lines. In Standalone the snapshot is `<dir>/backups/state-before-migration-12-<time>.db`.
3. `sisf doctor --dir "$D"`.

Verify: `ls -l "$D/backups"` shows the snapshot, readable by you only (`-rw-------`, in a `drwx------` directory), and the audit log has a `state.migrate_approved` record naming the migrations and the snapshot. An approval that names other versions, fewer or more, is refused with `APPROVAL_REQUIRED` and nothing is snapshot or applied; with no such migration pending the flag changes nothing. A snapshot that fails stops the upgrade before anything is migrated. Do not put the flag in a service unit: approve once, by hand.

Undo, with the factory stopped (`sisf stop --dir "$D"`) and the previous build checked out and installed (`make install`):

- Standalone: move the store aside and put the snapshot in its place: `mv "$D/data/state/factory.db" "$D/data/state/factory.db.migrated"`, remove `factory.db-wal` and `factory.db-shm` beside it, then `cp "$D/backups/<snapshot>.db" "$D/data/state/factory.db"`, and `sisf up --dir "$D"`. Observed in the tests: the snapshot opens at the version the store had before the migration, with its records (`internal/install/state_bootstrap_approved_migration_test.go`).
- Docker mode: start only the state service, replace the database with the dump, then start the factory:

  ```sh
  P="docker compose -p <project> -f $D/compose.yaml"
  $P up -d --wait state
  $P exec -T state dropdb --username=sisf_owner sisf
  $P exec -T state createdb --username=sisf_owner sisf
  $P exec -T state pg_restore --username=sisf_owner --dbname=sisf < "$D/backups/<snapshot>.dump"
  sisf up --dir "$D"
  ```

  Observed against PostgreSQL 16 with `docker exec` on a store `sisf state bootstrap` prepared: the restore brought back the rows, the schema version, the row policies and the zone roles' grants, and removed a table created after the dump. The same steps through `docker compose` on a whole Docker install have not been run.

## Renewing the supervisor's delegated keys

`sisf up` issues the supervisor's delegated signing keys from the root key. `sisf doctor` warns `delegated keys  they expire at <time>; run sisf up to issue new ones` as they near expiry and fails once one has expired. Stop the factory and run `sisf up` again. A running supervisor reads its delegated keys from the trust volume again every hour, and sooner when one nears its end, so it keeps signing once a replacement is there. It logs `a delegated key was replaced`, or, at warning level, `a delegated key could not be reloaded; the key in use is kept`; a key in use still refuses to sign once it has ended or is revoked (`cmd/supervisor/reloading_delegated_signer.go`).

## Removing an install

`sisf down -v --dir "$D"` stops the factory and removes its data: `<dir>/data` in Standalone, except the control zone's `local:` secret store in `data/control/secrets`, and in Docker mode the project's containers, networks and volumes, the `control-data` volume with that store included. It does so whether or not the factory runs. Secrets and `factory.yaml` are never removed by it; delete the directory yourself afterwards. Note that `-v` also removes the trust record. After a unit was killed rather than stopped, an agent CLI it started can outlive it without holding the run lock, so after a crash check that no agent process remains before using `-v` ([operations](operations.md#lifecycle)).
