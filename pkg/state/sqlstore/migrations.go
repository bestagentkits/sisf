package sqlstore

import (
	"embed"
	"fmt"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

//go:embed migrations/sqlite/*.sql migrations/postgres/*.sql
var migrationFiles embed.FS

// migration is one schema change with a script per dialect.
type migration struct {
	state.Migration
	// file is the script's name under migrations/<dialect>/.
	file string
	// inline replaces the files with one script for both dialects.
	inline string
}

// releaseMigrations are the migrations this release ships, in order. Add to
// the end only: an applied migration is never edited or renumbered.
//
// BackwardCompatible must be true only if the previous release keeps working
// on the schema after the migration (expand, never contract); the store of
// the previous release relies on that flag when it meets a newer schema.
var releaseMigrations = []migration{
	{
		Migration: state.Migration{Version: 1, Name: "operational schema and audit chain", BackwardCompatible: true},
		file:      "0001_operational_schema.sql",
	},
	{
		// Columns are only added, each with a default, so the previous release
		// reads and writes the new schema as it did the old one.
		Migration: state.Migration{Version: 2, Name: "candidate and release of a record", BackwardCompatible: true},
		file:      "0002_record_candidate_and_release.sql",
	},
	{
		// No data moves and the previous release's steps and documents are
		// all within the row policies, so it keeps working on the schema.
		Migration: state.Migration{Version: 3, Name: "row security between the zones", BackwardCompatible: true},
		file:      "0003_zone_row_security.sql",
	},
}

// Migrations lists the migrations this release ships, for callers that must
// judge a release before it runs, such as the gate that keeps a migration
// that is not backward compatible out of a self-published release.
func Migrations() []state.Migration {
	out := make([]state.Migration, len(releaseMigrations))
	for i, m := range releaseMigrations {
		out[i] = m.Migration
	}
	return out
}

// script returns the statements of the migration for one dialect.
func (m migration) script(d Dialect) (string, error) {
	if m.inline != "" {
		return m.inline, nil
	}
	content, err := migrationFiles.ReadFile("migrations/" + string(d) + "/" + m.file)
	if err != nil || len(content) == 0 {
		return "", core.NewError(core.CodeInternal,
			fmt.Sprintf("migration %d has no %s script", m.Version, string(d))).WithCause(err)
	}
	return string(content), nil
}
