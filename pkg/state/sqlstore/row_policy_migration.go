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

// usageLedgerPolicyScript is the PostgreSQL script of the migration that puts
// the usage ledger under row-level security and lets the agent zone read it
// (zonePrivileges): it enables row-level security on the ledger and, for each
// zone that holds a privilege on it, grants that privilege and writes the
// zone's policies (rowRule).
//
// As in keyedStepPolicyScript, the migration does not know the zone roles. It
// finds the role of a zone as the one its read policy on the records names,
// which every granted store has, and leaves a zone whose role was never
// granted as it is; granting writes the privileges and policies afresh. A
// store whose roles were granted by an earlier release so keeps metering
// usage, and its agent zone may total it, from the moment it is migrated.
func usageLedgerPolicyScript() (string, error) {
	var script strings.Builder
	script.WriteString(`ALTER TABLE ` + quote(tableUsage) + ` ENABLE ROW LEVEL SECURITY;
`)
	for _, zone := range core.Zones() {
		granted := zonePrivileges[zone][tableUsage]
		if granted == 0 {
			continue
		}
		if zonePrivileges[zone][tableRecords]&canSelect == 0 {
			return "", core.NewError(core.CodeInternal,
				"the role of the "+string(zone)+" zone cannot be found from a policy on the records")
		}
		script.WriteString(`DO $sisf_migration$
DECLARE
	holders text;
BEGIN
	SELECT string_agg(quote_ident(holder::text), ', ') INTO holders
	FROM pg_policies, unnest(roles) AS holder
	WHERE schemaname = current_schema() AND tablename = '` + tableRecords + `'
		AND policyname = '` + policyName(zone, "SELECT") + `' AND holder <> 'public';
	IF holders IS NULL THEN
		RETURN;
	END IF;
	EXECUTE 'GRANT ` + strings.Join(granted.names(), ", ") + ` ON ` + quote(tableUsage) + ` TO ' || holders;
`)
		for _, command := range policyCommands {
			if granted&command.bit == 0 {
				continue
			}
			clauses, err := policyClauses(tableUsage, command.bit, zone, quote(tableRecords))
			if err != nil {
				return "", err
			}
			if strings.Contains(clauses, "$") || strings.Contains(clauses, "'") {
				return "", core.NewError(core.CodeInternal, "the condition of a row policy cannot be quoted in a migration")
			}
			name := quote(policyName(zone, command.name))
			script.WriteString(`	EXECUTE 'DROP POLICY IF EXISTS ` + name + ` ON ` + quote(tableUsage) + `';
	EXECUTE 'CREATE POLICY ` + name + ` ON ` + quote(tableUsage) + ` FOR ` + command.name + ` TO ' || holders || '` + clauses + `';
`)
		}
		script.WriteString(`END
$sisf_migration$;
`)
	}
	return script.String(), nil
}

// recordStepScript is the PostgreSQL script of the migration that holds a
// record to one step of the lifecycle (recordStepTrigger) and writes afresh
// the policies of every zone on updating the records and journaling a step,
// for the steps the zones may write in this release (rowRule).
//
// As in keyedStepPolicyScript, the migration does not know the zone roles:
// each policy keeps the roles it names, and a schema whose roles were never
// granted is left with the trigger alone; granting writes the policies
// afresh.
func recordStepScript() (string, error) {
	trigger, err := recordStepTrigger()
	if err != nil {
		return "", err
	}
	var script strings.Builder
	script.WriteString(trigger)
	for _, rewrite := range []struct {
		table   string
		command privilege
		name    string
	}{{tableRecords, canUpdate, "UPDATE"}, {tableTransitions, canInsert, "INSERT"}} {
		for _, zone := range core.Zones() {
			if zonePrivileges[zone][rewrite.table]&rewrite.command == 0 {
				continue
			}
			clauses, err := policyClauses(rewrite.table, rewrite.command, zone, quote(tableRecords))
			if err != nil {
				return "", err
			}
			if strings.Contains(clauses, "$") {
				return "", core.NewError(core.CodeInternal, "the condition of a row policy cannot be quoted in a migration")
			}
			name := policyName(zone, rewrite.name)
			script.WriteString(`DO $sisf_migration$
DECLARE
	holders text;
BEGIN
	SELECT string_agg(quote_ident(holder::text), ', ') INTO holders
	FROM pg_policies, unnest(roles) AS holder
	WHERE schemaname = current_schema() AND tablename = '` + rewrite.table + `' AND policyname = '` + name + `';
	IF holders IS NULL THEN
		RETURN;
	END IF;
	EXECUTE 'DROP POLICY ` + quote(name) + ` ON ` + quote(rewrite.table) + `';
	EXECUTE 'CREATE POLICY ` + quote(name) + ` ON ` + quote(rewrite.table) + ` FOR ` + rewrite.name + ` TO ' || holders
		|| ` + stepPolicyScriptTag + clauses + stepPolicyScriptTag + `;
END
$sisf_migration$;
`)
		}
	}
	return script.String(), nil
}

// storeGatesScript is the PostgreSQL script of the migration that binds the
// journal row of a step to the record it writes, pairs every record write
// with that row, takes the submission of a candidate from the agent zone,
// holds the control zone off the agent zone's key prefixes, and puts the
// reservations and the usage ledger under budgetRowRules. It writes the
// record trigger and the zones' policies afresh (recordStepScript), which
// carries the new conditions on the journal and the agent zone's narrower
// states.
func storeGatesScript() (string, error) {
	steps, err := recordStepScript()
	if err != nil {
		return "", err
	}
	budget, err := budgetRowRules()
	if err != nil {
		return "", err
	}
	return steps + recordJournalTrigger() + budget, nil
}
