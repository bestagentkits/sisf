package sqlstore_test

import (
	"database/sql"
	"errors"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// The agent zone writes the steps of its runs only under its own keys, so it
// cannot take ahead of time the key of a step the control zone has yet to
// write and so block that step.
func TestAgentZoneWritesItsStepsOnlyUnderItsOwnKeys(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control := db.Open(t, core.ZoneControl)
		agent := db.Open(t, core.ZoneAgent)
		ready := readyRecord(t, control, issue(3))
		lease := leaseOn(t, agent, ready.IssueID, "worker-1")
		claimAs := func(key string) core.Transition {
			return next(t, ready, core.EventClaimed, func(tr *core.Transition) {
				under(lease)(tr)
				tr.RunID, tr.Attempt, tr.IdempotencyKey = "run-1", 1, key
			})
		}
		for name, key := range map[string]string{
			"an intake key of the control zone": "intake/" + ready.IssueID.String() + "/" + string(core.EventInfoSufficient),
			"a retry key of the control zone":   "retry/" + ready.IssueID.String(),
			"the key of another run step":       "develop/run-1",
			"the bare prefix of the step":       "claim/",
			"a key in no key space":             "run-1",
		} {
			refused(t, agent, "a claim under "+name, claimAs(key), core.CodePermissionDenied)
		}

		claimed := mustTransition(t, agent, claimAs("claim/run-1"))
		if claimed.State != core.StateClaimed || claimed.IdempotencyKey != "claim/run-1" {
			t.Fatalf("the claim under the agent zone's key = %+v", claimed)
		}
		// The control zone keeps the lifecycle and may key a run step as it
		// likes.
		advance(t, control, claimed, core.EventDevelopmentStarted, func(tr *core.Transition) {
			tr.IdempotencyKey = "control/" + string(core.EventDevelopmentStarted)
		})
	})
}

// A stolen agent-zone credential used with plain SQL may journal only a
// step of the agent zone's edges, under the agent zone's key for it, for the
// record it wrote. Each refused row breaks exactly one of those conditions;
// the accepted one meets them all, so the refusals are for the stated reason.
func TestPostgresHoldsTheAgentRoleToItsOwnStepsKeysAndRecords(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		agent := rawLogin(t, logins.DSN[core.ZoneAgent])
		assertAgentJournalRules(t, db, agent)
	})
}

// The migration rewrites the policy of a store whose roles were granted by
// an earlier release, keeping the role it names, so the store is held to
// the rules from the moment it is migrated.
func TestPostgresMigrationReplacesTheAgentStepPolicyOfAGrantedStore(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		raw := db.Raw(t)
		agentRole := logins.Roles[core.ZoneAgent]
		// The policy as the earlier release wrote it: the event alone.
		for _, statement := range []string{
			`DROP POLICY "sisf_agent_insert" ON transitions`,
			`CREATE POLICY "sisf_agent_insert" ON transitions FOR INSERT TO "` + agentRole + `"
				WITH CHECK (event IN ('candidate_submitted', 'claimed', 'development_started', 'qa_failed'))`,
		} {
			if _, err := raw.ExecContext(ctx, statement); err != nil {
				t.Fatalf("restore the earlier policy: %v", err)
			}
		}
		agent := rawLogin(t, logins.DSN[core.ZoneAgent])
		if _, err := agent.ExecContext(ctx, journalRow("acme/app#404", "Ready", "claimed", "Claimed", 1, "intake/acme/app#404/claimed")); err != nil {
			t.Fatalf("the earlier policy should let the forged row in, the premise of this test: %v", err)
		}

		script, err := sqlstore.MigrationScript(5, sqlstore.DialectPostgres)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.ExecContext(ctx, script); err != nil {
			t.Fatalf("apply the migration to a granted store: %v", err)
		}
		var roles string
		if err := raw.QueryRowContext(ctx, `SELECT array_to_string(roles, ',') FROM pg_policies
			WHERE schemaname = current_schema() AND tablename = 'transitions' AND policyname = 'sisf_agent_insert'`).Scan(&roles); err != nil {
			t.Fatalf("read the rewritten policy: %v", err)
		}
		if roles != agentRole {
			t.Fatalf("the rewritten policy names %q, want the agent role %q", roles, agentRole)
		}
		assertAgentJournalRules(t, db, agent)
	})
}

// assertAgentJournalRules checks the agent role's journal policy over the
// agent role's own connection.
func assertAgentJournalRules(t *testing.T, db *sqlstoretest.Database, agent *sql.DB) {
	t.Helper()
	owner := db.Open(t, core.ZoneControl)
	raw := db.Raw(t)
	ready := readyRecord(t, owner, issue(5))
	ref := ready.IssueID.String()
	// standAt puts the record where a step the test journals leaves it, as
	// the owner, past the store, so each row below differs from an
	// acceptable one in a single way.
	standAt := func(state core.State, version int64, key string) {
		t.Helper()
		if _, err := raw.ExecContext(ctx, `UPDATE records SET state = $1, version = $2, idempotency_key = $3
			WHERE factory_id = $4 AND issue = $5`, string(state), version, key, string(sqlstoretest.Factory), ref); err != nil {
			t.Fatalf("place the record: %v", err)
		}
	}
	version := ready.Version + 1

	standAt(core.StateClaimed, version, "intake/"+ref+"/claimed")
	refusedRow(t, agent, "a control-zone key", journalRow(ref, "Ready", "claimed", "Claimed", version, "intake/"+ref+"/claimed"))
	standAt(core.StateClaimed, version, "develop/run-1")
	refusedRow(t, agent, "the key of another step", journalRow(ref, "Ready", "claimed", "Claimed", version, "develop/run-1"))

	standAt(core.StateClaimed, version, "claim/run-1")
	refusedRow(t, agent, "an edge the lifecycle does not have", journalRow(ref, "Developing", "claimed", "Claimed", version, "claim/run-1"))
	standAt(core.StateReleaseCandidate, version, "claim/run-1")
	refusedRow(t, agent, "an edge of the control zone under an agent key", journalRow(ref, "QAEvaluation", "qa_passed", "ReleaseCandidate", version, "claim/run-1"))
	standAt(core.StateClaimed, version, "claim/run-1")
	refusedRow(t, agent, "a record that does not exist", journalRow("acme/app#404", "Ready", "claimed", "Claimed", 1, "claim/run-1"))
	refusedRow(t, agent, "a version the record is not at", journalRow(ref, "Ready", "claimed", "Claimed", version+1, "claim/run-1"))

	if _, err := agent.ExecContext(ctx, journalRow(ref, "Ready", "claimed", "Claimed", version, "claim/run-1")); err != nil {
		t.Fatalf("the agent role must journal its own step of its own record: %v", err)
	}
}

// journalRow is an insert of one step into the journal.
func journalRow(issue, from, event, to string, version int64, key string) string {
	return `INSERT INTO transitions (factory_id, issue, idempotency_key, from_state, to_state, event, version,
		lease_owner, fencing_token, attempt, run_id, actor_id, reason, evidence_refs, candidate_digest, release_digest, created_at)
		VALUES ('` + string(sqlstoretest.Factory) + `', '` + issue + `', '` + key + `', '` + from + `', '` + to + `', '` + event + `', ` +
		strconv.FormatInt(version, 10) + `, 'worker-1', 1, 1, 'run-1', 'agent', '', '[]', '', '', 0)`
}

// refusedRow fails the test unless the database refuses statement with
// insufficient_privilege.
func refusedRow(t *testing.T, db *sql.DB, name, statement string) {
	t.Helper()
	_, err := db.ExecContext(ctx, statement)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "42501" {
		t.Errorf("the agent role must be refused %s, got %v", name, err)
	}
}

// rawLogin connects with a zone's own login, past the store.
func rawLogin(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
