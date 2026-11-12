-- The lifecycle gains the step from Verified to Blocked, for an issue whose
-- verified release can no longer be judged to be what serves, healthy, and
-- that no later release replaced, SQLite dialect. On PostgreSQL the
-- migration writes the trigger that holds a record to one step of the
-- lifecycle and the zones' row policies afresh; its script is built from
-- the store's rules (row_policy_migration.go), so it has no file.
--
-- SQLite has no roles and no row policies. The store checks the same rules
-- on every step it writes, and the schema does not change.
SELECT 1;
