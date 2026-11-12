package harness

import (
	"context"
	"database/sql"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Journey returns the states issue number went through, in order, as the
// store's transition journal records them. It reads the SQLite file over a
// read-only connection of its own; the store has no API for the journal
// because nothing in the factory reads it back.
func (f *Factory) Journey(number int) []core.State {
	f.t.Helper()
	db, err := sql.Open("sqlite", "file:"+f.dsn+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(context.Background(),
		`SELECT to_state FROM transitions WHERE factory_id = ? AND issue = ? ORDER BY version`,
		string(f.Manifest.FactoryID), f.Issue(number).String())
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var states []core.State
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			f.t.Fatal(err)
		}
		states = append(states, core.State(s))
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return states
}
