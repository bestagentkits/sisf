package sqlstore

// recordJournalFunction names the trigger of recordJournalTrigger and its
// function.
const recordJournalFunction = "sisf_record_journaled"

// recordJournalTrigger returns the statements that install the trigger which
// holds every write of a record, by a session that row-level security holds,
// to the journal row of its step, as the store writes the two in one
// transaction (Transition): when the transaction commits, the journal has a
// row for the issue under the record's key, at its version and state, that
// keeps the record as written (journal).
//
// The record trigger (recordStepTrigger) and the row policies say which
// steps a zone may take; without this trigger a zone role could take one
// and journal nothing. The step would leave no trace, and a claim would
// leave its run out of the journal, which is where the store and the record
// trigger look to refuse a claim that brings back a run the issue had.
//
// The trigger is a constraint trigger deferred to the commit, since the
// store writes the record before the journal row. It checks the record as
// that write left it, so a transaction that takes two steps of a record is
// held to the journal row of each.
//
// As in recordStepTrigger, the owner of the table, a superuser and a role
// that bypasses row security are left alone, and the function runs with
// pg_catalog alone on its search path.
func recordJournalTrigger() string {
	return `CREATE OR REPLACE FUNCTION ` + quote(recordJournalFunction) + `() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $sisf_record_journaled$
DECLARE
	journaled boolean;
BEGIN
	IF pg_has_role(current_user, (SELECT relowner FROM pg_class WHERE oid = TG_RELID), 'USAGE')
		OR EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND rolbypassrls) THEN
		RETURN NULL;
	END IF;
	EXECUTE format('SELECT EXISTS (SELECT 1 FROM %I.` + quote(tableTransitions) + ` AS j
		WHERE j.factory_id = $1 AND j.issue = $2 AND j.idempotency_key = $3 AND j.version = $4 AND j.to_state = $5
			AND j.run_id = $6 AND j.lease_owner = $7 AND j.fencing_token = $8 AND j.attempt = $9
			AND j.record_candidate_digest = $10 AND j.record_release_digest = $11 AND j.created_at = $12)', TG_TABLE_SCHEMA)
		INTO journaled USING NEW.factory_id, NEW.issue, NEW.idempotency_key, NEW.version, NEW.state,
			NEW.run_id, NEW.lease_owner, NEW.fencing_token, NEW.attempt,
			NEW.candidate_digest, NEW.release_digest, NEW.updated_at;
	IF NOT journaled THEN
		RAISE EXCEPTION 'a lifecycle record changes only together with the journal row of its step' USING ERRCODE = 'insufficient_privilege';
	END IF;
	RETURN NULL;
END
$sisf_record_journaled$;
DROP TRIGGER IF EXISTS ` + quote(recordJournalFunction) + ` ON ` + quote(tableRecords) + `;
CREATE CONSTRAINT TRIGGER ` + quote(recordJournalFunction) + ` AFTER INSERT OR UPDATE ON ` + quote(tableRecords) + `
	DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION ` + quote(recordJournalFunction) + `();
`
}
