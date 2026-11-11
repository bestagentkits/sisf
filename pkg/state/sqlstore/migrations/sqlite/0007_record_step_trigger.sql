-- A lifecycle record moves only along one step of the lifecycle, and the
-- agent zone no longer writes a failed evaluation, SQLite dialect. On
-- PostgreSQL the migration installs a trigger on the records and writes the
-- zones' row policies afresh; its script is built from the store's rules
-- (row_policy_migration.go), so it has no file.
--
-- SQLite has no roles and no row policies. The store checks the same rules
-- on every step it writes, and the schema does not change.
SELECT 1;
