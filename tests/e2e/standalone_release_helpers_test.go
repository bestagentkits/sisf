//go:build e2e

package e2e

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The release half of the Standalone deployment: the scripted agent's patch,
// and the store's records and key-value entries read from the install's
// SQLite file.

// queryRows runs one read-only statement on the install's store and hands
// every row to scan.
func (d *standaloneDeployment) queryRows(t *testing.T, scan func(*sql.Rows) error, statement string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+d.stateDB()+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(statement, args...)
	if err != nil {
		t.Fatalf("query the store: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := scan(rows); err != nil {
			t.Fatal(err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func (d *standaloneDeployment) records(t *testing.T) []issueRecord {
	t.Helper()
	var out []issueRecord
	d.queryRows(t, func(rows *sql.Rows) error {
		var r issueRecord
		if err := rows.Scan(&r.Issue, &r.State, &r.RunID, &r.Attempt); err != nil {
			return err
		}
		out = append(out, r)
		return nil
	}, `SELECT issue, state, run_id, attempt FROM records WHERE factory_id = ?`, string(d.m.FactoryID))
	return out
}

func (d *standaloneDeployment) kv(t *testing.T, namespace, key string) string {
	t.Helper()
	var value string
	d.queryRows(t, func(rows *sql.Rows) error { return rows.Scan(&value) },
		`SELECT CAST(entry_value AS TEXT) FROM kv WHERE factory_id = ? AND namespace = ? AND entry_key = ?`,
		string(d.m.FactoryID), namespace, key)
	return value
}

func (d *standaloneDeployment) candidateDigest(t *testing.T, issue string) string {
	t.Helper()
	var digest string
	d.queryRows(t, func(rows *sql.Rows) error { return rows.Scan(&digest) },
		`SELECT candidate_digest FROM transitions WHERE factory_id = ? AND issue = ? AND candidate_digest <> '' ORDER BY version DESC LIMIT 1`,
		string(d.m.FactoryID), issue)
	if digest == "" {
		t.Fatalf("%s has no candidate", issue)
	}
	return digest
}

func (d *standaloneDeployment) edgeURL() string { return "http://" + standaloneEdge }

// setAgentPatch rewrites the patch the dispatcher reads at its start, kills
// the dispatcher, the scripted agent's process, and waits until `sisf up`
// has started it again with the new patch.
func (d *standaloneDeployment) setAgentPatch(t *testing.T, diff string) {
	t.Helper()
	path := filepath.Join(d.home, "sim", "agent", "patch.diff")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sisf up wrote no scripted patch at %s", path)
	}
	if err := os.WriteFile(path, []byte(diff), 0o644); err != nil {
		t.Fatal(err)
	}
	d.killDispatcher(t)
	d.startDispatcher(t)
	eventually(t, "the restarted dispatcher's health", func() (bool, string) {
		_, err := d.dispatcherHealth(t)
		return err == nil, describeErr(err)
	})
}

func describeErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
