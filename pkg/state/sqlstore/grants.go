package sqlstore

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// ZoneRoles names the PostgreSQL role each zone connects with.
type ZoneRoles map[core.Zone]string

// identifierPattern is what a role or schema name must look like before it
// is written into a statement. Statements cannot take identifiers as
// parameters, so anything else is refused instead of escaped.
var identifierPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// GrantStatements returns the statements that give each zone's role exactly
// what its zone may reach in schema, and nothing else. Each role first
// loses whatever it held on the factory's tables, so running the statements
// again also takes away what a later release no longer grants.
//
// There must be one role for each of the three zones and no two zones may
// share one: a shared role would let one zone act as another.
func GrantStatements(schema string, roles ZoneRoles) ([]string, error) {
	if !identifierPattern.MatchString(schema) {
		return nil, invalid("schema name %q must be a plain lowercase identifier", schema)
	}
	if len(roles) != len(core.Zones()) {
		return nil, invalid("the store needs one role for each of the three zones")
	}
	seen := map[string]core.Zone{}
	for _, zone := range core.Zones() {
		role := roles[zone]
		if !identifierPattern.MatchString(role) {
			return nil, invalid("role name %q of the %s zone must be a plain lowercase identifier", role, string(zone))
		}
		if other, taken := seen[role]; taken {
			return nil, invalid("the %s and %s zones must not share the store role %q", string(other), string(zone), role)
		}
		seen[role] = zone
	}

	qualified := make([]string, len(tables))
	for i, table := range tables {
		qualified[i] = quote(schema) + "." + quote(table)
	}
	var statements []string
	for _, zone := range core.Zones() {
		role := quote(roles[zone])
		statements = append(statements,
			fmt.Sprintf(`REVOKE ALL PRIVILEGES ON TABLE %s FROM %s`, strings.Join(qualified, ", "), role),
			fmt.Sprintf(`GRANT USAGE ON SCHEMA %s TO %s`, quote(schema), role))
		for i, table := range tables {
			if granted := zonePrivileges[zone][table]; granted != 0 {
				statements = append(statements, fmt.Sprintf(`GRANT %s ON TABLE %s TO %s`,
					strings.Join(granted.names(), ", "), qualified[i], role))
			}
		}
	}
	return statements, nil
}

// GrantZoneRoles applies GrantStatements to the schema the store is
// connected to, in one transaction. The roles must exist already; creating
// them and their credentials belongs to the install. It needs a connection
// that owns the tables and a store opened for the control zone.
//
// SQLite has no roles. There the zones are kept apart by this package's own
// checks and by the permissions of the database file, and the call is
// refused with NOT_SUPPORTED instead of pretending to have done something.
func (s *Store) GrantZoneRoles(ctx context.Context, roles ZoneRoles) error {
	if s.zone != core.ZoneControl {
		return core.NewError(core.CodePermissionDenied,
			fmt.Sprintf("the %s zone may not change store roles", string(s.zone)))
	}
	if s.dialect != DialectPostgres {
		return core.NewError(core.CodeNotSupported, "the "+string(s.dialect)+" store has no database roles to grant")
	}
	return s.inRawTx(ctx, false, func(ctx context.Context, q *querier) error {
		var schema string
		if err := q.row(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
			return s.dialect.failure("read schema name", err)
		}
		statements, err := GrantStatements(schema, roles)
		if err != nil {
			return err
		}
		for _, statement := range statements {
			if err := q.script(ctx, "grant zone roles", statement); err != nil {
				return err
			}
		}
		return nil
	})
}

// quote renders a checked identifier for use in a statement.
func quote(identifier string) string { return `"` + identifier + `"` }
