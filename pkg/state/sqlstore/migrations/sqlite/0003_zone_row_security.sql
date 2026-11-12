-- Rows of the lifecycle and of the documents divided between the zones,
-- SQLite dialect. The PostgreSQL script next to this one enables row-level
-- security on records, transitions and kv.
--
-- SQLite has no roles, so there is no session to key a rule on. The zones
-- are kept apart by the store's own checks and by the permissions of the
-- database file, as for every other table, and the schema does not change.
SELECT 1;
