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
	// postgres replaces the PostgreSQL file with a script built from this
	// package's rules, for a migration that rewrites a row policy.
	postgres func() (string, error)
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
	{
		// Columns are only added, each with a default, and a delivery that
		// leaves the inbox for good is marked processed as before, so the
		// previous release neither retries a parked delivery nor fails on
		// the new columns. It ignores the retry schedule and tries a failing
		// delivery again at its own pace, as it always did. The inbox has no
		// row policies and its grants are per table, so no grant changes.
		Migration: state.Migration{Version: 4, Name: "retry schedule and outcome of inbox deliveries", BackwardCompatible: true},
		file:      "0004_inbox_retry_schedule.sql",
	},
	{
		// Only the condition of the agent zone's policy on the journal
		// changes, and the steps of the previous release's dispatcher are
		// within it, so it keeps working on the schema.
		Migration: state.Migration{Version: 5, Name: "agent steps keyed and bound to their records", BackwardCompatible: true},
		file:      "0005_agent_step_row_policy.sql",
		postgres:  keyedStepPolicyScript,
	},
	{
		// The ledger gains row policies that let every zone keep what it held
		// and a read for the agent zone; no data moves and no column changes.
		// The previous release reads and appends usage as before, and granting
		// its roles again only takes the agent zone's read away.
		Migration: state.Migration{Version: 6, Name: "agent zone reads the usage ledger", BackwardCompatible: true},
		file:      "0006_agent_reads_usage_ledger.sql",
		postgres:  usageLedgerPolicyScript,
	},
	{
		// A trigger holds every update of a record by a zone role to one
		// step of the lifecycle, and the zones' policies are written for
		// this release's steps, which take the failed evaluation from the
		// agent zone. No data moves and no column changes. Every step the
		// previous release writes is an edge to the next version, and its
		// agent zone never wrote a failed evaluation, so it keeps working
		// on the schema.
		Migration: state.Migration{Version: 7, Name: "records move only along lifecycle steps", BackwardCompatible: true},
		file:      "0007_record_step_trigger.sql",
		postgres:  recordStepScript,
	},
	{
		// The lifecycle gains the two steps that block an issue whose run
		// stopped, written by the control zone only. The trigger and the
		// zones' policies are written afresh for them; no data moves and no
		// column changes. The previous release never writes the new steps,
		// every step it does write is still an edge, and it knows Blocked,
		// so it keeps working on the schema.
		Migration: state.Migration{Version: 8, Name: "steps that block an issue whose run stopped", BackwardCompatible: true},
		file:      "0008_run_stop_steps.sql",
		postgres:  recordStepScript,
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
	if d == DialectPostgres && m.postgres != nil {
		return m.postgres()
	}
	content, err := migrationFiles.ReadFile("migrations/" + string(d) + "/" + m.file)
	if err != nil || len(content) == 0 {
		return "", core.NewError(core.CodeInternal,
			fmt.Sprintf("migration %d has no %s script", m.Version, string(d))).WithCause(err)
	}
	return string(content), nil
}
