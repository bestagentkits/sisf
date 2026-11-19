# ADR 0013: Schema migration on self-update

## Status

Status: proposed for the self-publish gate, which stays an open point; the parts listed under Decision as implemented are accepted. Closes open point 4 of brief [§15](../self-improvement-software-factory-decision-brief.en.md). The Owner delegated this decision; it was taken on that delegation and recorded here and on [#76](https://github.com/bestagentkits/sisf/issues/76).

## Context

Code rollback has rules. Rollback after a database schema change does not.

v0.1.0-alpha shipped migration 12, the first one marked not backward compatible: it takes UPDATE on the reservations from the agent zone's PostgreSQL role, which the previous release's dispatcher needs to claim an issue. Until now a store that has migrations applied could not take such a migration at all, because no command passed the Owner's approval to the migrator.

## Decision

Implemented:

- Migrations are expand/contract whenever possible, compatible across the current and previous version (N and N-1). Each carries a `backward_compatible` flag, stored with it in `schema_migrations`; a build refuses to run on a schema that has an incompatible migration it does not know.
- A database that holds no store yet (no migration recorded and no `records` table) takes every migration: no release runs on it and a rollback has no data to keep.
- On an existing store, a migration that is not backward compatible needs the Owner's approval, and nothing is applied without it (`APPROVAL_REQUIRED`).
- The Owner approves on the factory's host, by running `sisf up --approve-migrations=<versions>`. The approval names the exact versions: it is accepted only when it equals the set of incompatible migrations pending on the store, checked under the migration lock, so an approval of 12 never admits a later 13. When no incompatible migration is pending the flag changes nothing.
- Before an approved migration runs, `sisf up` writes a snapshot of the whole store to `<dir>/backups/` (directory 0700, file 0600): a `VACUUM INTO` copy of the SQLite file in Standalone, a `pg_dump --format=custom` of the database, taken through the state container, in Docker mode. A snapshot that fails stops the upgrade before anything is migrated. The maintenance guide gives the restore.
- The applied approval is written to the audit log: the Owner as actor on the CLI surface, the versions and the snapshot's file name.

Why on the host and not through a signed approval of the action service: the approval is needed exactly when the factory cannot run. The new build refuses to open a store that has an incompatible migration pending, and the old build does not know the migration, so no running surface can take the request. Running `sisf up` already requires the Owner's access to the host and to the install directory, which holds the root key, so it is the Owner's act in the same sense as the install itself. This is a deliberate exception to surface parity, limited to install-time steps that run before any surface exists.

Proposed, not built:

- An incompatible migration can never ride along with a self-published release. Today the factory self-publishes only application units (`release.units`); SISF itself, and with it the state store's migrations, changes only when the Owner runs `sisf up` with a new build. So no self-published release can carry a store migration yet, and the gate is needed only once SISF updates itself.

## Consequences

- Some changes take two releases.
- A rollback never needs a data restore for compatible migrations.
- An incompatible migration costs the Owner one explicit command and leaves a snapshot to return to; going back means restoring that snapshot together with the previous build, since there are no down migrations.

## Acceptance test

- `pkg/state/sqlstore`: an approval naming exactly the pending incompatible migrations applies them; a partial, extra or other set is refused and nothing is applied; an approval of one version does not admit a later incompatible one.
- `internal/install`: an approved Standalone upgrade writes the snapshot before migrating, a failed snapshot leaves the schema where it was, the snapshot restores the store as it was, and the audit log names the approval. Docker mode takes the snapshot through the state container before the state step runs.
