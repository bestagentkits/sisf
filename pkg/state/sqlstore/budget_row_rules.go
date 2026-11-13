package sqlstore

import (
	"github.com/bestagentkits/sisf/pkg/core"
)

// reservationFunction names the trigger of budgetRowRules and its function.
const reservationFunction = "sisf_reservation_run"

// budgetRowRules returns the statements that hold the reservations and the
// usage ledger, in the database itself, to what PutReservation and
// AppendUsage write, so a stolen zone credential used with plain SQL can
// neither free money set aside for a run nor lower what was spent:
//
//   - a reservation never moves to another factory, ID, issue, run or claim,
//     and keeps the instant it was made at (a trigger, which leaves the
//     owner of the table, a superuser and a role that bypasses row security
//     alone, as recordStepTrigger does);
//   - a reservation is made under the idempotency key of its claim, sets
//     aside no negative amount, and is in a known budget mode and status
//     (constraints);
//   - usage never has a negative cost or count of tokens (constraints).
//
// The constraints hold every session, as the store refuses those values to
// every zone. The statements may run again, as a migration written afresh
// does. The constraints are added NOT VALID: they hold every row written from
// now on, and a row an earlier release let in does not stop the migration.
//
// The database scopes no table by factory, since a zone role is granted per
// schema and not per factory; the store reads and writes only the rows of
// its own factory.
func budgetRowRules() (string, error) {
	modes, err := inList("budget_mode", []string{string(core.BudgetUnlimited), string(core.BudgetCapped)}, false)
	if err != nil {
		return "", err
	}
	statuses, err := inList("status", []string{
		string(core.ReservationHeld), string(core.ReservationSettled),
		string(core.ReservationReleased), string(core.ReservationSuspended),
	}, false)
	if err != nil {
		return "", err
	}
	reservations, usage := quote(tableReservations), quote(tableUsage)
	return `ALTER TABLE ` + reservations + ` DROP CONSTRAINT IF EXISTS "sisf_reservation_values";
ALTER TABLE ` + reservations + ` ADD CONSTRAINT "sisf_reservation_values"
	CHECK (amount >= 0 AND idempotency_key <> '' AND ` + modes + ` AND ` + statuses + `) NOT VALID;
ALTER TABLE ` + usage + ` DROP CONSTRAINT IF EXISTS "sisf_usage_values";
ALTER TABLE ` + usage + ` ADD CONSTRAINT "sisf_usage_values"
	CHECK (cost >= 0 AND input_tokens >= 0 AND output_tokens >= 0) NOT VALID;
CREATE OR REPLACE FUNCTION ` + quote(reservationFunction) + `() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $sisf_reservation_run$
BEGIN
	IF pg_has_role(current_user, (SELECT relowner FROM pg_class WHERE oid = TG_RELID), 'USAGE')
		OR EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND rolbypassrls) THEN
		RETURN NEW;
	END IF;
	IF NEW.factory_id = OLD.factory_id AND NEW.id = OLD.id AND NEW.issue = OLD.issue AND NEW.run_id = OLD.run_id
		AND NEW.idempotency_key = OLD.idempotency_key AND NEW.created_at = OLD.created_at THEN
		RETURN NEW;
	END IF;
	RAISE EXCEPTION 'a reservation stays the money of the run it was made for' USING ERRCODE = 'insufficient_privilege';
END
$sisf_reservation_run$;
DROP TRIGGER IF EXISTS ` + quote(reservationFunction) + ` ON ` + reservations + `;
CREATE TRIGGER ` + quote(reservationFunction) + ` BEFORE UPDATE ON ` + reservations + `
	FOR EACH ROW EXECUTE FUNCTION ` + quote(reservationFunction) + `();
`, nil
}
