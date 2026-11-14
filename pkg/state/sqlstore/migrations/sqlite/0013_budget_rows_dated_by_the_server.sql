-- The usage ledger is indexed by run, which a run's close reads, SQLite
-- dialect. On PostgreSQL the migration also dates every reservation and
-- usage row a zone role writes by the server's clock and writes the zones'
-- policies on the ledger afresh; that script is built from the store's
-- rules (budget_row_time.go), so it has no file.
--
-- SQLite has no roles and no row policies. The store dates the agent zone's
-- reservations and usage by its own clock and checks the same rules on
-- every write.
CREATE INDEX IF NOT EXISTS usage_ledger_run ON usage_ledger (factory_id, run_id, recorded_at);
