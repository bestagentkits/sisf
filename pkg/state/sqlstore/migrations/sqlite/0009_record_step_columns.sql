-- A lifecycle record changes its run, attempt, candidate, release and lease
-- only as the step it moves along does, SQLite dialect.
-- On PostgreSQL the migration writes afresh the trigger that holds a record
-- to one step of the lifecycle, and the zones' row policies; its script is
-- built from the store's rules (record_step_trigger.go and
-- row_policy_migration.go), so it has no file.
--
-- SQLite has no roles and no row policies. The store checks the same rules
-- on every step it writes, and the schema does not change.
SELECT 1;
