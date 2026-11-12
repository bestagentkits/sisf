-- Rows of the lifecycle and of the documents divided between the zones,
-- PostgreSQL dialect. The SQLite script next to this one is the same
-- migration for an engine without roles.
--
-- The agent zone moves only the records of its runs and keeps only its own
-- documents; the control zone keeps the rest. With row-level security on,
-- a zone role reaches only the rows a policy gives it. The policies name the
-- zone roles, so they are written with the grants (GrantZoneRoles); until
-- then a zone role reaches no row of these tables, which fails closed. The
-- owner of the tables is not subject to the policies, and no data moves, so
-- the previous release keeps working on the same tables.
ALTER TABLE records ENABLE ROW LEVEL SECURITY;
ALTER TABLE transitions ENABLE ROW LEVEL SECURITY;
ALTER TABLE kv ENABLE ROW LEVEL SECURITY;
