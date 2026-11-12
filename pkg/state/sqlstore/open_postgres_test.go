package sqlstore

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

// These checks parse connection strings only; nothing connects to a server.

func TestPostgresArgumentsAreNeverPastedIntoStatementText(t *testing.T) {
	t.Parallel()
	for name, dsn := range map[string]string{
		"URL asking for the simple protocol":      "postgres://sisf_control@db.internal:5432/sisf?default_query_exec_mode=simple_protocol",
		"keywords asking for the simple protocol": "host=db.internal user=sisf_control dbname=sisf default_query_exec_mode=simple_protocol",
	} {
		config, err := postgresConfig(dsn)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if config.DefaultQueryExecMode == pgx.QueryExecModeSimpleProtocol {
			t.Errorf("%s: the driver would paste arguments into statement text", name)
		}
	}
	// Modes that send arguments apart from the statement are left alone.
	for dsn, want := range map[string]pgx.QueryExecMode{
		"postgres://sisf_control@db.internal/sisf":                              pgx.QueryExecModeCacheStatement,
		"postgres://sisf_control@db.internal/sisf?default_query_exec_mode=exec": pgx.QueryExecModeExec,
	} {
		config, err := postgresConfig(dsn)
		if err != nil {
			t.Fatalf("%s: %v", dsn, err)
		}
		if config.DefaultQueryExecMode != want {
			t.Errorf("%s: mode %v, want %v", dsn, config.DefaultQueryExecMode, want)
		}
	}
	if _, err := postgresConfig("postgres://db.internal:not-a-port/sisf"); err == nil {
		t.Error("a malformed connection string must be refused")
	}
}
