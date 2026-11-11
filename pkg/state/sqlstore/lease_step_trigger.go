package sqlstore

// leaseStepFunction names the trigger of leaseStepTrigger and its function.
const leaseStepFunction = "sisf_lease_step"

// leaseStepTrigger returns the statements that install the trigger which
// holds every change of a lease, by a session that row-level security
// holds, to one of the writes the store's lease API makes (leases.go):
//
//   - AcquireLease inserts a lease at token 1, or takes one over by
//     raising its token by exactly one and naming a holder;
//   - RenewLease moves the expiry of a lease that has a holder, keeping
//     the holder and the token;
//   - ReleaseLease empties the holder and the expiry and keeps the token.
//
// The factory and the resource of a lease never change, and nothing else
// does either. The record trigger (recordStepTrigger) admits a lifecycle
// step only under the latest token of the issue lease; without this
// trigger a zone role could set that token to anything with plain SQL and
// so meet the rule without taking the lease. Now the only way to a new
// token is a takeover, which names its holder and fences off the one
// before, exactly as AcquireLease does.
//
// When a takeover is allowed (the lease is free, has expired, or is asked
// for again by its holder) depends on the store's clock, which the
// database does not share, so the trigger does not judge it. Nor does it
// bind a holder name to a zone, which the lease API does not do either.
//
// As in recordStepTrigger, the owner of the table, a superuser and a role
// that bypasses row security are left alone: they install, migrate and
// repair the schema.
func leaseStepTrigger() string {
	refuse := `RAISE EXCEPTION 'a lease is taken, renewed or given up only as the store does it' USING ERRCODE = 'insufficient_privilege';`
	return `CREATE OR REPLACE FUNCTION ` + quote(leaseStepFunction) + `() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $sisf_lease_step$
BEGIN
	IF pg_has_role(current_user, (SELECT relowner FROM pg_class WHERE oid = TG_RELID), 'USAGE')
		OR EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND rolbypassrls) THEN
		RETURN NEW;
	END IF;
	IF TG_OP = 'INSERT' THEN
		IF NEW.token = 1 AND NEW.holder <> '' AND NEW.expires_at > 0 THEN
			RETURN NEW;
		END IF;
		` + refuse + `
	END IF;
	IF NEW.factory_id = OLD.factory_id AND NEW.resource = OLD.resource AND (
		(NEW.token = OLD.token + 1 AND NEW.holder <> '' AND NEW.expires_at > 0)
		OR (NEW.token = OLD.token AND NEW.holder = OLD.holder AND OLD.holder <> '' AND NEW.expires_at > 0)
		OR (NEW.token = OLD.token AND NEW.holder = '' AND NEW.expires_at = 0 AND OLD.holder <> '')) THEN
		RETURN NEW;
	END IF;
	` + refuse + `
END
$sisf_lease_step$;
DROP TRIGGER IF EXISTS ` + quote(leaseStepFunction) + ` ON ` + quote(tableLeases) + `;
CREATE TRIGGER ` + quote(leaseStepFunction) + ` BEFORE INSERT OR UPDATE ON ` + quote(tableLeases) + `
	FOR EACH ROW EXECUTE FUNCTION ` + quote(leaseStepFunction) + `();
`
}

// releaseRefusalScript is the PostgreSQL script of the migration that adds
// the step that blocks an issue whose release was refused: the record
// trigger and the zones' policies afresh (recordStepScript), and the
// trigger that holds the leases to the writes of the lease API
// (leaseStepTrigger).
func releaseRefusalScript() (string, error) {
	steps, err := recordStepScript()
	if err != nil {
		return "", err
	}
	return steps + leaseStepTrigger(), nil
}
