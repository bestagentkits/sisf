package sqlstore

import (
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// TestMigration is a schema change a test pretends a later release ships.
// Its one script must be valid on both dialects.
type TestMigration struct {
	Name               string
	BackwardCompatible bool
	SQL                string
}

// AddReleaseMigrations makes the store behave like a later release that
// ships more migrations than this one, numbered after those it already has.
func AddReleaseMigrations(s *Store, more ...TestMigration) {
	migrations := slices.Clone(s.migrations)
	for _, m := range more {
		migrations = append(migrations, migration{
			Migration: state.Migration{Version: len(migrations) + 1, Name: m.Name, BackwardCompatible: m.BackwardCompatible},
			inline:    m.SQL,
		})
	}
	s.migrations = migrations
	s.schemaChecked.Store(false)
}

// MigrationScript returns the script a shipped migration runs on a dialect.
func MigrationScript(version int, d Dialect) (string, error) {
	return releaseMigrations[version-1].script(d)
}

// MigrationLock is the statement and argument that take the migration lock
// of the schema a connection's search path names, as Migrate does.
func MigrationLock() (string, int32) {
	return strings.Replace(lockMigrations, "?", "$1", 1), migrationLockClass
}

// ZoneMayWrite reports whether a store of zone may write a step on event.
func ZoneMayWrite(zone core.Zone, event core.Event) bool { return zoneEvents[zone][event] }

// StepKeyPrefix is the prefix of the idempotency keys under which zone writes
// the step on event; empty when the zone has no keys of its own for it.
func StepKeyPrefix(zone core.Zone, event core.Event) string { return zoneStepKeys[zone][event] }
