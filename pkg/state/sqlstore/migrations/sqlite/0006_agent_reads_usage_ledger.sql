-- The agent zone reads the usage ledger of its factory, so that a capped
-- budget can admit a run against the spending recorded so far, SQLite
-- dialect. On PostgreSQL the migration puts the ledger under row-level
-- security and grants the read with its policy; its script is built from
-- the store's rules (row_policy_migration.go), so it has no file.
--
-- SQLite has no roles and no row policies. The store checks the zone's
-- privileges on every operation, and the schema does not change.
SELECT 1;
