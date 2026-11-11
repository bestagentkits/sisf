-- The lifecycle gains the steps from Claimed and from Developing to Blocked,
-- for an issue whose run stopped without a candidate, SQLite dialect. On
-- PostgreSQL the migration writes the trigger that holds a record to one
-- step of the lifecycle, and the zones' row policies, afresh for the steps
-- of this release; its script is built from the store's rules
-- (row_policy_migration.go), so it has no file.
--
-- SQLite has no roles and no row policies. The store checks the same rules
-- on every step it writes, and the schema does not change.
SELECT 1;
