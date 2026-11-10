package sqlstore

import (
	"fmt"

	"github.com/bestagentkits/sisf/pkg/core"
)

// privilege is a set of things a zone may do to a table.
type privilege uint8

const (
	canSelect privilege = 1 << iota
	canInsert
	canUpdate
	canDelete
)

// The tables of the operational schema.
const (
	tableRecords      = "records"
	tableTransitions  = "transitions"
	tableLeases       = "leases"
	tableInbox        = "inbox"
	tableOutbox       = "outbox"
	tableReservations = "reservations"
	tableUsage        = "usage_ledger"
	tableKV           = "kv"
	tableAuditLog     = "audit_log"
	tableAuditHead    = "audit_head"
	tableMigrations   = "schema_migrations"
)

// tables lists every table in a fixed order, for statements that name them all.
var tables = []string{
	tableRecords, tableTransitions, tableLeases, tableInbox, tableOutbox,
	tableReservations, tableUsage, tableKV, tableAuditLog, tableAuditHead, tableMigrations,
}

// factoryWork is what the agent zone and the control zone share: the working
// state of issues and runs. Usage and the audit log are insert-only for
// every zone, so nothing the factory runs can rewrite its own history.
var factoryWork = map[string]privilege{
	tableRecords:      canSelect | canInsert | canUpdate,
	tableTransitions:  canSelect | canInsert,
	tableLeases:       canSelect | canInsert | canUpdate,
	tableOutbox:       canSelect | canInsert | canUpdate,
	tableReservations: canSelect | canInsert | canUpdate,
	tableUsage:        canSelect | canInsert,
	tableKV:           canSelect | canInsert | canUpdate | canDelete,
	tableAuditLog:     canSelect | canInsert,
	tableAuditHead:    canSelect | canInsert | canUpdate,
	tableMigrations:   canSelect,
}

// zonePrivileges is the single statement of what each zone may reach. The
// store checks it before every operation, and the PostgreSQL grants are
// generated from it, so the two cannot drift apart.
//
// Zone 1, the application, gets nothing but the schema version: it has no
// path into the factory's state. Zone 2 and Zone 3 both work on issues and
// runs. Only Zone 3 stores a delivery, because an inbox entry is trusted as
// a verified webhook and Zone 3 is where webhooks are verified; Zone 2 may
// read deliveries and mark them processed.
var zonePrivileges = map[core.Zone]map[string]privilege{
	core.ZoneApplication: {tableMigrations: canSelect},
	core.ZoneAgent:       withTable(factoryWork, tableInbox, canSelect|canUpdate),
	core.ZoneControl:     withTable(factoryWork, tableInbox, canSelect|canInsert|canUpdate),
}

// withTable returns a copy of base with one more table.
func withTable(base map[string]privilege, table string, p privilege) map[string]privilege {
	out := make(map[string]privilege, len(base)+1)
	for name, granted := range base {
		out[name] = granted
	}
	out[table] = p
	return out
}

// names renders a privilege set as SQL keywords in a fixed order.
func (p privilege) names() []string {
	var out []string
	for _, known := range []struct {
		bit  privilege
		name string
	}{{canSelect, "SELECT"}, {canInsert, "INSERT"}, {canUpdate, "UPDATE"}, {canDelete, "DELETE"}} {
		if p&known.bit != 0 {
			out = append(out, known.name)
		}
	}
	return out
}

// allowed reports whether the zone of the store may do all of need to table.
func (s *Store) allowed(table string, need privilege) error {
	if zonePrivileges[s.zone][table]&need == need {
		return nil
	}
	return core.NewError(core.CodePermissionDenied,
		fmt.Sprintf("the %s zone may not use %s in the state store this way", string(s.zone), table))
}

// reads checks that the transaction's zone may read every table given.
func (t *tx) reads(tableNames ...string) error {
	for _, table := range tableNames {
		if err := t.store.allowed(table, canSelect); err != nil {
			return err
		}
	}
	return nil
}

// writes checks that the transaction is read-write and that its zone holds
// all of need on table.
func (t *tx) writes(table string, need privilege) error {
	if err := t.store.allowed(table, need); err != nil {
		return err
	}
	if t.readOnly {
		return invalid("a read-only transaction cannot change %s", table)
	}
	return nil
}
