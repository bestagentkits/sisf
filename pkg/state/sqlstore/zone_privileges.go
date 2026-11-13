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

// zonePrivileges is the single statement of what each zone may reach. The
// store checks it before every operation, and the PostgreSQL grants are
// generated from it, so the two cannot drift apart. Which rows of the
// lifecycle and of the documents a zone may touch is narrowed further by
// the rules in zone_ownership.go.
//
// Zone 1, the application, gets nothing but the schema version: it has no
// path into the factory's state.
//
// Zone 2, the agent factory, gets what the dispatcher uses and nothing
// more: it reads the lifecycle records and moves those of its runs, holds
// leases, reserves budget and meters usage, and keeps its own documents.
// It makes the reservation of a claim and changes none: closing a run's
// reservation is the control plane's and the supervisor's, so a stolen
// agent credential cannot free money a live run set aside. It
// reads the usage ledger because a capped budget admits a run only against
// the spending recorded so far; it never changes a ledger row.
// It creates no record and deletes nothing, and it does not touch the
// inbox, the outbox or the audit log: deliveries, notifications and audit
// records are the control plane's, reached through its authenticated API.
//
// Zone 3, the control plane, keeps the lifecycle. Only it stores a
// delivery, because an inbox entry is trusted as a verified webhook and
// Zone 3 is where webhooks are verified.
//
// Usage and the audit log are insert-only for every zone, so nothing the
// factory runs can rewrite its own history.
var zonePrivileges = map[core.Zone]map[string]privilege{
	core.ZoneApplication: {tableMigrations: canSelect},
	core.ZoneAgent: {
		tableRecords:      canSelect | canUpdate,
		tableTransitions:  canSelect | canInsert,
		tableLeases:       canSelect | canInsert | canUpdate,
		tableReservations: canSelect | canInsert,
		tableUsage:        canSelect | canInsert,
		tableKV:           canSelect | canInsert | canUpdate,
		tableMigrations:   canSelect,
	},
	core.ZoneControl: {
		tableRecords:      canSelect | canInsert | canUpdate,
		tableTransitions:  canSelect | canInsert,
		tableLeases:       canSelect | canInsert | canUpdate,
		tableInbox:        canSelect | canInsert | canUpdate,
		tableOutbox:       canSelect | canInsert | canUpdate,
		tableReservations: canSelect | canInsert | canUpdate,
		tableUsage:        canSelect | canInsert,
		tableKV:           canSelect | canInsert | canUpdate | canDelete,
		tableAuditLog:     canSelect | canInsert,
		tableAuditHead:    canSelect | canInsert | canUpdate,
		tableMigrations:   canSelect,
	},
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
