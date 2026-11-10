package sqlstore

import (
	"slices"

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
