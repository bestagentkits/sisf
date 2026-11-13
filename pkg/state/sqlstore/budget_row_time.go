package sqlstore

// budgetTimeFunction names the trigger of budgetRowTimeRules and its
// function.
const budgetTimeFunction = "sisf_budget_row_time"

// budgetRowTimeRules returns the statements that date, in the database
// itself, every reservation and usage row a zone role writes by the
// database server's clock, in the nanoseconds the store writes, whatever
// date the writer gave. A run's close counts the usage rows recorded since
// its reservation was made and a budget period the rows recorded since it
// began, so a writer that could date its own rows could make a reservation
// in the past and book usage that lowers the run's charge while counting in
// no period. The server's clock_timestamp is read for each row, not the
// start of its transaction, so a row is never dated before a reservation
// committed while its transaction ran.
//
// The owner of the table, a superuser and a role that bypasses row security
// keep the date they write, as budgetRowRules leaves them alone: they are
// not zone roles, and the store run as one dates rows by its own clock.
func budgetRowTimeRules() string {
	function := quote(budgetTimeFunction)
	return `CREATE OR REPLACE FUNCTION ` + function + `() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $sisf_budget_row_time$
DECLARE
	server_now bigint := CAST(EXTRACT(EPOCH FROM clock_timestamp()) * 1000000000 AS bigint);
BEGIN
	IF pg_has_role(current_user, (SELECT relowner FROM pg_class WHERE oid = TG_RELID), 'USAGE')
		OR EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND rolbypassrls) THEN
		RETURN NEW;
	END IF;
	IF TG_TABLE_NAME = '` + tableReservations + `' THEN
		NEW.created_at := server_now;
	ELSE
		NEW.recorded_at := server_now;
	END IF;
	RETURN NEW;
END
$sisf_budget_row_time$;
DROP TRIGGER IF EXISTS ` + function + ` ON ` + quote(tableReservations) + `;
CREATE TRIGGER ` + function + ` BEFORE INSERT ON ` + quote(tableReservations) + `
	FOR EACH ROW EXECUTE FUNCTION ` + function + `();
DROP TRIGGER IF EXISTS ` + function + ` ON ` + quote(tableUsage) + `;
CREATE TRIGGER ` + function + ` BEFORE INSERT ON ` + quote(tableUsage) + `
	FOR EACH ROW EXECUTE FUNCTION ` + function + `();
`
}

// usageRunIndex is the statement of the index a run's close reads the
// usage of one run through.
const usageRunIndex = `CREATE INDEX IF NOT EXISTS usage_ledger_run ON usage_ledger (factory_id, run_id, recorded_at);
`

// budgetTimeAndUsageScript is the PostgreSQL script of the migration that
// dates the budget's rows by the server (budgetRowTimeRules), writes afresh
// the zones' policies on the usage ledger for this release's rules
// (usageLedgerPolicyScript) and indexes the ledger by run.
func budgetTimeAndUsageScript() (string, error) {
	policies, err := usageLedgerPolicyScript()
	if err != nil {
		return "", err
	}
	return budgetRowTimeRules() + policies + usageRunIndex, nil
}
