-- The agent zone journals only its own edges of the lifecycle, under its own
-- idempotency keys and for records it wrote, SQLite dialect. On PostgreSQL
-- the migration rewrites the condition of the agent zone's row policy on
-- transitions; its script is built from the store's rules
-- (row_policy_migration.go), so it has no file.
--
-- SQLite has no roles and no row policies. The store checks the same rules
-- on every step it writes, and the schema does not change.
SELECT 1;
