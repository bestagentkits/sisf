package sqlstore

import (
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// stepPolicyScriptTag delimits the condition inside the migration script.
const stepPolicyScriptTag = "$sisf_check$"

// keyedStepPolicyScript is the PostgreSQL script of the migration that
// replaces the policy under which each zone with keys of its own
// (zoneStepKeys) journals its steps, with the condition of this release
// (keyedStepCheck).
//
// A migration does not know the zone roles: they are named at install, and
// GrantZoneRoles writes the policies with the grants. So the script keeps
// the roles a policy names and changes only its condition, and leaves a
// schema whose roles were never granted as it is; granting writes the
// policy afresh. A store is thus held to the new condition from the moment
// it is migrated, before the grants are run again.
func keyedStepPolicyScript() (string, error) {
	var script strings.Builder
	for _, zone := range core.Zones() {
		if _, keyed := zoneStepKeys[zone]; !keyed {
			continue
		}
		// The table is named as the other migrations name it, through the
		// search path of the migrating connection.
		check, err := keyedStepCheck(zone, quote(tableRecords))
		if err != nil {
			return "", err
		}
		if strings.Contains(check, "$") {
			return "", core.NewError(core.CodeInternal, "the condition of a row policy cannot be quoted in a migration")
		}
		name := policyName(zone, "INSERT")
		script.WriteString(`DO $sisf_migration$
DECLARE
	holders text;
BEGIN
	SELECT string_agg(quote_ident(holder::text), ', ') INTO holders
	FROM pg_policies, unnest(roles) AS holder
	WHERE schemaname = current_schema() AND tablename = '` + tableTransitions + `' AND policyname = '` + name + `';
	IF holders IS NULL THEN
		RETURN;
	END IF;
	EXECUTE 'DROP POLICY ` + quote(name) + ` ON ` + quote(tableTransitions) + `';
	EXECUTE 'CREATE POLICY ` + quote(name) + ` ON ` + quote(tableTransitions) + ` FOR INSERT TO ' || holders
		|| ' WITH CHECK (' || ` + stepPolicyScriptTag + check + stepPolicyScriptTag + ` || ')';
END
$sisf_migration$;
`)
	}
	return script.String(), nil
}
