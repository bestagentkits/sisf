package sqlstore

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
)

// On PostgreSQL the database enforces the rules of zone_ownership.go itself,
// with row-level security on the tables those rules divide by row: the
// lifecycle records, their journal and the documents (migration 3), and the
// usage ledger, which the agent zone reads but never changes (migration 6).
// The policies name the zone roles, so they are written with the grants,
// from the same rules the store checks.
//
// Row-level security is used rather than a table per zone or triggers. It
// splits no data, so the previous release keeps working on the same tables;
// it covers reads as well as writes, which no trigger can; and a policy
// names its role directly, where a trigger would have to learn the zone of
// the session from somewhere. A row the zone may not read is invisible to
// its role, a row it may not change is left alone, and a row it may not
// write is refused with insufficient_privilege. The owner of the tables,
// who installs and migrates the schema, is not subject to the policies.
//
// A policy sees each row on its own, never the change from the old row to
// the new one, so it cannot say that an update is one step of the
// lifecycle. A trigger on the records says it (migration 7, again migration
// 8, migration 10 and migration 11 for the steps added since, and migration 9 for what
// each step may change; recordStepTrigger): for every session the policies hold, a record
// moves from one state to the next only along an edge of the lifecycle, to
// the next version, stays the record of its factory and issue, and changes
// its run, attempt, candidate, release and lease only as that step does in
// the store. With the policy of a zone on the states its steps
// start and end in, that admits exactly the zone's own steps.
var rowPolicyTables = []string{tableRecords, tableTransitions, tableKV, tableUsage}

// policyCommands are the commands a policy is written for, one per
// privilege, in a fixed order.
var policyCommands = []struct {
	bit  privilege
	name string
}{{canSelect, "SELECT"}, {canInsert, "INSERT"}, {canUpdate, "UPDATE"}, {canDelete, "DELETE"}}

// literalPattern is what a value must look like before it is written into
// a policy as a literal: a state, an event or a namespace of this package.
var literalPattern = regexp.MustCompile(`^[A-Za-z0-9_.]+$`)

// keyPrefixPattern is what an idempotency key prefix must look like before
// it is written into a policy as a literal.
var keyPrefixPattern = regexp.MustCompile(`^[a-z]+/$`)

// policyStatements returns the statements that replace the row policies of
// every zone. Each table first loses the policies of all zones, so running
// them again also takes away what a later release no longer allows.
func policyStatements(schema string, roles ZoneRoles) ([]string, error) {
	var statements []string
	for _, table := range rowPolicyTables {
		qualified := quote(schema) + "." + quote(table)
		for _, zone := range core.Zones() {
			for _, command := range policyCommands {
				name := quote(policyName(zone, command.name))
				statements = append(statements, fmt.Sprintf(`DROP POLICY IF EXISTS %s ON %s`, name, qualified))
				if zonePrivileges[zone][table]&command.bit == 0 {
					continue
				}
				clauses, err := policyClauses(table, command.bit, zone, quote(schema)+"."+quote(tableRecords))
				if err != nil {
					return nil, err
				}
				statements = append(statements, fmt.Sprintf(`CREATE POLICY %s ON %s FOR %s TO %s%s`,
					name, qualified, command.name, quote(roles[zone]), clauses))
			}
		}
	}
	return statements, nil
}

// policyClauses renders the USING and WITH CHECK clauses of the policy of
// zone for one command on table, each with a leading space, from rowRule.
func policyClauses(table string, command privilege, zone core.Zone, records string) (string, error) {
	using, check, err := rowRule(table, command, zone, records)
	if err != nil {
		return "", err
	}
	// A read or a removal only selects rows, and an insert only leaves one
	// behind.
	switch command {
	case canSelect, canDelete:
		check = ""
	case canInsert:
		using = ""
	}
	var clauses string
	if using != "" {
		clauses += " USING (" + using + ")"
	}
	if check != "" {
		clauses += " WITH CHECK (" + check + ")"
	}
	return clauses, nil
}

// policyName names the policy of one zone and command; it is the same on
// every table.
func policyName(zone core.Zone, command string) string {
	return "sisf_" + string(zone) + "_" + strings.ToLower(command)
}

// rowRule returns the conditions of the policy of zone for one command on
// table: the rows the zone may see or change (USING) and the rows it may
// leave behind (WITH CHECK). An empty condition is not written. records is
// how the conditions name the records table.
func rowRule(table string, command privilege, zone core.Zone, records string) (using, check string, err error) {
	switch table {
	case tableRecords:
		created, from, to := stepStates(zone)
		switch command {
		case canSelect:
			return "true", "", nil
		case canInsert:
			check, err = inList("state", created, false)
			return "", check, err
		case canUpdate:
			// A policy sees each row on its own, not the step from one to
			// the other: the zone may change the records that stand where
			// its steps start and leave them only where its steps end.
			if using, err = inList("state", from, false); err != nil {
				return "", "", err
			}
			check, err = inList("state", to, false)
			return using, check, err
		}
	case tableTransitions:
		switch command {
		case canSelect:
			return "true", "", nil
		case canInsert:
			if _, keyed := zoneStepKeys[zone]; keyed {
				check, err = keyedStepCheck(zone, records)
				return "", check, err
			}
			check, err = inList("event", eventNames(zone), false)
			return "", check, err
		}
	case tableUsage:
		// The ledger is not divided between the zones by row: a zone that
		// holds a privilege on it holds it on every row, as on the records.
		// The database scopes no table by factory, since a zone role is
		// granted per schema and not per factory; the store reads and writes
		// only the rows of its own factory. Changing or removing a row is
		// granted to no zone, and a trigger refuses it to the owner as well.
		switch command {
		case canSelect:
			return "true", "", nil
		case canInsert:
			return "", "true", nil
		}
	case tableKV:
		read, readNegated := namespaces(zone, kvNamespace.mayRead)
		write, writeNegated := namespaces(zone, kvNamespace.mayWrite)
		readable, err := inList("namespace", read, readNegated)
		if err != nil {
			return "", "", err
		}
		writable, err := inList("namespace", write, writeNegated)
		if err != nil {
			return "", "", err
		}
		switch command {
		case canSelect:
			return readable, "", nil
		case canInsert:
			return "", writable, nil
		case canUpdate:
			return writable, writable, nil
		case canDelete:
			return writable, "", nil
		}
	}
	// A privilege granted on a table without a rule for it lets no row
	// through, so a grant added without a rule fails closed.
	return "false", "false", nil
}

// stepStates returns, for the steps zone may write, the states a step
// creates a record in, the states the other steps start from and the
// states they end in.
func stepStates(zone core.Zone) (created, from, to []string) {
	for _, edge := range statemachine.Edges() {
		if !zoneEvents[zone][edge.Event] {
			continue
		}
		if edge.From == "" {
			created = appendNew(created, string(edge.To))
			continue
		}
		from, to = appendNew(from, string(edge.From)), appendNew(to, string(edge.To))
	}
	return created, from, to
}

// keyedStepCheck is the condition on a step a zone with keys of its own
// journals: the step is one of the zone's edges of the lifecycle, taken
// under the key prefix of its event (zoneStepKeys), and it journals the
// record as written: the record of the issue stands in the state the step
// ends in, at the version and under the key of the step. A stolen
// credential can so neither take a key of another zone, nor journal a step
// the lifecycle has no edge for, nor one for a record it did not write.
func keyedStepCheck(zone core.Zone, records string) (string, error) {
	var edges []string
	for _, edge := range statemachine.Edges() {
		if !zoneEvents[zone][edge.Event] {
			continue
		}
		prefix, listed := zoneStepKeys[zone][edge.Event]
		if !listed {
			continue
		}
		// A step that creates its record starts from no state.
		from := "''"
		if edge.From != "" {
			var err error
			if from, err = literal(string(edge.From), literalPattern); err != nil {
				return "", err
			}
		}
		event, err := literal(string(edge.Event), literalPattern)
		if err != nil {
			return "", err
		}
		to, err := literal(string(edge.To), literalPattern)
		if err != nil {
			return "", err
		}
		quotedPrefix, err := literal(prefix, keyPrefixPattern)
		if err != nil {
			return "", err
		}
		edges = append(edges, fmt.Sprintf(
			"(from_state = %s AND event = %s AND to_state = %s AND starts_with(idempotency_key, %s) AND length(idempotency_key) > %d)",
			from, event, to, quotedPrefix, len(prefix)))
	}
	if len(edges) == 0 {
		return "false", nil
	}
	slices.Sort(edges)
	return "(" + strings.Join(edges, " OR ") + ") AND EXISTS (SELECT 1 FROM " + records + " AS r" +
		" WHERE r.factory_id = transitions.factory_id AND r.issue = transitions.issue" +
		" AND r.state = transitions.to_state AND r.version = transitions.version" +
		" AND r.idempotency_key = transitions.idempotency_key)", nil
}

// eventNames lists the events zone may write.
func eventNames(zone core.Zone) []string {
	var names []string
	for event := range zoneEvents[zone] {
		names = append(names, string(event))
	}
	return names
}

// namespaces describes the namespaces allowed reports for zone as a list:
// the listed namespaces it is allowed, or, when it is allowed the namespaces
// nobody listed, the listed ones it is not (negated is then true).
func namespaces(zone core.Zone, allowed func(kvNamespace, core.Zone) bool) (names []string, negated bool) {
	negated = allowed(kvNamespace{owner: namespaceOwner}, zone)
	for name, rule := range kvNamespaces {
		if allowed(rule, zone) != negated {
			names = append(names, name)
		}
	}
	return names, negated
}

// inList renders "column IN (values)", or NOT IN when negated, with each
// value checked and quoted as a literal, in a fixed order.
func inList(column string, values []string, negated bool) (string, error) {
	if len(values) == 0 {
		return strconv.FormatBool(negated), nil
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	quoted := make([]string, len(sorted))
	for i, value := range sorted {
		var err error
		if quoted[i], err = literal(value, literalPattern); err != nil {
			return "", err
		}
	}
	operator := " IN ("
	if negated {
		operator = " NOT IN ("
	}
	return column + operator + strings.Join(quoted, ", ") + ")", nil
}

// literal quotes value for a policy once it matches pattern.
func literal(value string, pattern *regexp.Regexp) (string, error) {
	if !pattern.MatchString(value) {
		return "", core.NewError(core.CodeInternal, fmt.Sprintf("%q cannot be written into a row policy", value))
	}
	return "'" + value + "'", nil
}

// appendNew appends value unless list holds it already.
func appendNew(list []string, value string) []string {
	if slices.Contains(list, value) {
		return list
	}
	return append(list, value)
}
