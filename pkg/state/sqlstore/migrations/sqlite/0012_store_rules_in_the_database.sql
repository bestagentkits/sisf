-- The journal row of a step keeps the record as the step wrote it, a record
-- commits only with that row, the zones keep to their own key prefixes, and
-- the reservations and the usage ledger refuse what the store refuses,
-- SQLite dialect. On PostgreSQL the migration writes the triggers, the
-- constraints and the zones' row policies; its script is built from the
-- store's rules (row_policy_migration.go), so it has no file.
--
-- SQLite has no roles and no row policies. The store checks the same rules
-- on every write, and the schema does not change.
SELECT 1;
