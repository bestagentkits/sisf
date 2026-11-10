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
// with row-level security on the three tables those rules divide by row:
// the lifecycle records, their journal and the documents. Migration 3
// enables it; the policies name the zone roles, so they are written with the
// grants, from the same rules the store checks.
//
// Row-level security is used rather than a table per zone or triggers. It
// splits no data, so the previous release keeps working on the same tables;
// it covers reads as well as writes, which no trigger can; and a policy
// names its role directly, where a trigger would have to learn the zone of
// the session from somewhere. A row the zone may not read is invisible to
// its role, a row it may not change is left alone, and a row it may not
// write is refused with insufficient_privilege. The owner of the tables,
// who installs and migrates the schema, is not subject to the policies.
var rowPolicyTables = []string{tableRecords, tableTransitions, tableKV}

// policyCommands are the commands a policy is written for, one per
// privilege, in a fixed order.
var policyCommands = []struct {
	bit  privilege
	name string
}{{canSelect, "SELECT"}, {canInsert, "INSERT"}, {canUpdate, "UPDATE"}, {canDelete, "DELETE"}}

// literalPattern is what a value must look like before it is written into
// a policy as a literal: a state, an event or a namespace of this package.
var literalPattern = regexp.MustCompile(`^[A-Za-z0-9_.]+$`)

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
				using, check, err := rowRule(table, command.bit, zone)
				if err != nil {
					return nil, err
				}
				// A read or a removal only selects rows, and an insert only
				// leaves one behind.
				switch command.bit {
				case canSelect, canDelete:
					check = ""
				case canInsert:
					using = ""
				}
				statement := fmt.Sprintf(`CREATE POLICY %s ON %s FOR %s TO %s`, name, qualified, command.name, quote(roles[zone]))
				if using != "" {
					statement += " USING (" + using + ")"
				}
				if check != "" {
					statement += " WITH CHECK (" + check + ")"
				}
				statements = append(statements, statement)
			}
		}
	}
	return statements, nil
}

// policyName names the policy of one zone and command; it is the same on
// every table.
func policyName(zone core.Zone, command string) string {
	return "sisf_" + string(zone) + "_" + strings.ToLower(command)
}

// rowRule returns the conditions of the policy of zone for one command on
// table: the rows the zone may see or change (USING) and the rows it may
// leave behind (WITH CHECK). An empty condition is not written.
func rowRule(table string, command privilege, zone core.Zone) (using, check string, err error) {
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
			check, err = inList("event", eventNames(zone), false)
			return "", check, err
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
		if !literalPattern.MatchString(value) {
			return "", core.NewError(core.CodeInternal, fmt.Sprintf("%q cannot be written into a row policy", value))
		}
		quoted[i] = "'" + value + "'"
	}
	operator := " IN ("
	if negated {
		operator = " NOT IN ("
	}
	return column + operator + strings.Join(quoted, ", ") + ")", nil
}

// appendNew appends value unless list holds it already.
func appendNew(list []string, value string) []string {
	if slices.Contains(list, value) {
		return list
	}
	return append(list, value)
}
