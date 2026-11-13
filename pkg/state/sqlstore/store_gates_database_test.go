package sqlstore_test

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// A candidate is named by the trusted build in the control zone, which
// builds it from the run's patch. A run that could submit a candidate itself
// could put a digest on the record the build never produced, and take ahead
// of time the key under which the build submits it.
func TestAgentZoneDoesNotSubmitACandidate(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control := db.Open(t, core.ZoneControl)
		agent := db.Open(t, core.ZoneAgent)
		developed := along(t, agent, claim(t, agent, readyRecord(t, control, issue(3)), "worker-1", "run-1"),
			core.EventDevelopmentStarted)
		submit := next(t, developed, core.EventCandidateSubmitted, func(tr *core.Transition) {
			tr.IdempotencyKey = "candidate/run-1/1"
		})
		refused(t, agent, "a candidate submitted by the agent zone", submit, core.CodePermissionDenied)
		if submitted := mustTransition(t, control, submit); submitted.CandidateDigest != candidateDigest {
			t.Fatalf("the control zone's submission wrote %+v", submitted)
		}
	})
}

// The agent zone holds key prefixes of its own for its steps. The control
// zone, which may key its steps as it likes, never writes another step under
// one of them, so the agent zone cannot take ahead of time the key of a step
// the control zone has yet to write.
func TestControlZoneWritesNoOtherStepUnderAnAgentKey(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control := db.Open(t, core.ZoneControl)
		intake := received(issue(4))
		intake.IdempotencyKey = "claim/run-1"
		refused(t, control, "an intake step under the agent zone's claim key", intake, core.CodePermissionDenied)
		intake.IdempotencyKey = "develop/run-1"
		refused(t, control, "an intake step under the agent zone's development key", intake, core.CodePermissionDenied)

		claimed := claim(t, control, readyRecord(t, control, issue(5)), "worker-1", "run-1")
		refused(t, control, "a start of development under the agent zone's claim key",
			next(t, claimed, core.EventDevelopmentStarted, func(tr *core.Transition) { tr.IdempotencyKey = "claim/run-1-again" }),
			core.CodePermissionDenied)
		// The agent zone's own step under its own key stays the control
		// zone's to write as well.
		advance(t, control, claimed, core.EventDevelopmentStarted, func(tr *core.Transition) { tr.IdempotencyKey = "develop/run-1" })
	})
}

// The same rule holds the control role used with plain SQL, and its
// journal rows, like the agent role's, keep the record as the step wrote it:
// no row stands for a step that wrote no record.
func TestPostgresHoldsTheControlRoleOffTheAgentKeysAndToItsRecords(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		assertControlJournalRules(t, db, rawLogin(t, logins.DSN[core.ZoneControl]))
	})
}

// assertControlJournalRules checks the control role's journal policy over
// its own connection, on the intake of an issue whose record the owner puts
// in place, past the store.
func assertControlJournalRules(t *testing.T, db *sqlstoretest.Database, control *sql.DB) {
	t.Helper()
	const ref = "acme/app#41"
	place := func(key string) {
		t.Helper()
		for _, statement := range []string{
			`DELETE FROM records WHERE factory_id = '` + string(sqlstoretest.Factory) + `' AND issue = '` + ref + `'`,
			`INSERT INTO records (factory_id, issue, state, version, idempotency_key, lease_owner, fencing_token, attempt,
				run_id, candidate_digest, release_digest, updated_at)
				VALUES ('` + string(sqlstoretest.Factory) + `', '` + ref + `', 'Intake', 1, '` + key + `', '', 0, 0, '', '', '', 7)`,
		} {
			if _, err := db.Raw(t).ExecContext(ctx, statement); err != nil {
				t.Fatalf("place the record: %v", err)
			}
		}
	}
	received := func(key string) journalInsert {
		return journalRow(ref, "", "issue_received", "Intake", 1, key).
			with("lease_owner", "''").with("fencing_token", "0").with("attempt", "0").with("run_id", "''")
	}
	place("claim/run-1")
	refusedRow(t, control, "an intake step under the agent zone's claim key", received("claim/run-1").sql())
	place("develop/run-1")
	refusedRow(t, control, "an intake step under the agent zone's development key", received("develop/run-1").sql())

	const key = "intake/acme/app#41/issue_received"
	place(key)
	refusedRow(t, control, "a step of an issue with no record", journalRow("acme/app#404", "", "issue_received", "Intake", 1, key).
		with("lease_owner", "''").with("fencing_token", "0").with("attempt", "0").with("run_id", "''").sql())
	// The journal names only steps the lifecycle has: an event of the zone
	// on an edge it does not end or start on is refused, though the record
	// stands where the row says.
	refusedRow(t, control, "a claim the lifecycle has no edge into Intake for",
		received(key).with("event", "'claimed'").with("from_state", "'Ready'").sql())
	refusedRow(t, control, "an intake from a state the issue_received edge does not start from",
		received(key).with("from_state", "'Ready'").sql())
	for name, column := range map[string][2]string{
		"a run the record does not name":       {"run_id", "'run-2'"},
		"a lease the record does not name":     {"lease_owner", "'worker-2'"},
		"another version":                      {"version", "2"},
		"another state":                        {"to_state", "'Clarifying'"},
		"another time":                         {"created_at", "8"},
		"a candidate the record does not name": {"candidate_digest", "'" + string(candidateDigest) + "'"},
	} {
		refusedRow(t, control, "a journal row with "+name, received(key).with(column[0], column[1]).sql())
	}
	if _, err := control.ExecContext(ctx, received(key).sql()); err != nil {
		t.Fatalf("the control role must journal a step under its own key, for its record: %v", err)
	}
}

// A record changes only together with the journal row of its step, as the
// store writes them in one transaction: a stolen credential cannot move a
// record and leave no trace of the step, nor leave a run out of the journal
// that tells a claim which runs the issue has had.
func TestPostgresCommitsARecordStepOnlyWithItsJournalRow(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		agent := rawLogin(t, logins.DSN[core.ZoneAgent])
		control := db.Open(t, core.ZoneControl)
		claimed := claim(t, control, readyRecord(t, control, issue(6)), "worker-1", "run-1")
		refusedAtCommit(t, agent, "a record step with no journal row",
			recordUpdate(`state = 'Developing', version = version + 1, idempotency_key = 'develop/run-1'`, claimed.IssueID))
		if got := record(t, control, claimed.IssueID); got != claimed {
			t.Fatalf("a refused step moved the record to %+v", got)
		}
		rawStep(t, agent, "the agent role's start of development", claimed.IssueID,
			`state = 'Developing', version = version + 1, idempotency_key = 'develop/run-1'`, core.StateClaimed, core.EventDevelopmentStarted)
		if got := record(t, control, claimed.IssueID); got.State != core.StateDeveloping {
			t.Fatalf("the agent role's step with its journal row left the record in %s", got.State)
		}
	})
}

// The agent role makes a reservation, held, and changes none: closing a
// run's reservation is the control plane's and the supervisor's. A zone role
// that may change a reservation keeps it the money of the run it was made
// for, on the terms it was made with, and only moves its status on; and
// neither a reservation nor metered usage can lower what the budget counts.
func TestPostgresHoldsTheZoneRolesToTheReservationsAndUsageTheyMayWrite(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		agentStore := db.OpenWith(t, logins.DSN[core.ZoneAgent], core.ZoneAgent)
		mustUpdate(t, agentStore, func(tx state.Tx) error { return tx.PutReservation(ctx, reservation("r-1", 500)) })
		agent := rawLogin(t, logins.DSN[core.ZoneAgent])
		control := rawLogin(t, logins.DSN[core.ZoneControl])
		reservationOf := func(set string) string {
			return `UPDATE reservations SET ` + set + ` WHERE factory_id = '` + string(sqlstoretest.Factory) + `' AND id = 'r-1'`
		}
		usage := func(cost, input, output string) string {
			return `INSERT INTO usage_ledger (factory_id, issue, run_id, reservation_id, step, model_route,
				input_tokens, output_tokens, cost, verified, recorded_at) VALUES ('` + string(sqlstoretest.Factory) +
				`', 'acme/app#7', 'run-1', 'r-1', 'coding', '{}', ` + input + `, ` + output + `, ` + cost + `, false, 1)`
		}
		newReservation := func(id, amount, key, mode, status string) string {
			return `INSERT INTO reservations (factory_id, id, issue, run_id, budget_mode, amount, status, hard_cap_enforced,
				idempotency_key, created_at) VALUES ('` + string(sqlstoretest.Factory) + `', '` + id + `', 'acme/app#7', 'run-1',
				'` + mode + `', ` + amount + `, '` + status + `', true, '` + key + `', 1)`
		}
		// The agent role closes no reservation and changes none.
		for name, statement := range map[string]string{
			"a reservation released by the agent role": reservationOf(`status = 'released'`),
			"a reservation settled by the agent role":  reservationOf(`status = 'settled'`),
			"a reservation lowered by the agent role":  reservationOf(`amount = 0`),
		} {
			refusedRow(t, agent, name, statement)
		}
		// A reservation made other than held is refused to every zone role
		// that may make one, and a change of hands or terms or a status that
		// goes back to the one that may change one.
		for name, check := range map[string]struct {
			role      *sql.DB
			statement string
		}{
			"a reservation made settled":                     {agent, newReservation("r-5", "1", "claim-r-5", "capped", "settled")},
			"a reservation made released":                    {agent, newReservation("r-6", "1", "claim-r-6", "capped", "released")},
			"a reservation made released by control":         {control, newReservation("r-8", "1", "claim-r-8", "capped", "released")},
			"a reservation lowered":                          {control, reservationOf(`amount = 0`)},
			"a negative reservation":                         {control, reservationOf(`amount = -1`)},
			"a reservation made unlimited":                   {control, reservationOf(`budget_mode = 'unlimited'`)},
			"a reservation that drops the provider hard cap": {control, reservationOf(`hard_cap_enforced = false`)},
			"a reservation in no known status":               {control, reservationOf(`status = 'forgotten'`)},
			"a reservation in no known budget mode":          {control, reservationOf(`budget_mode = 'free'`)},
			"a reservation handed to another run":            {control, reservationOf(`run_id = 'run-9'`)},
			"a reservation handed to another issue":          {control, reservationOf(`issue = 'acme/app#9'`)},
			"a reservation handed to another claim":          {control, reservationOf(`idempotency_key = 'claim-other'`)},
			"a reservation moved to another factory":         {control, reservationOf(`factory_id = 'other'`)},
			"a reservation renamed":                          {control, reservationOf(`id = 'r-2'`)},
			"a reservation backdated":                        {control, reservationOf(`created_at = 0`)},
		} {
			refusedRow(t, check.role, name, check.statement)
		}
		// A value no store writes is refused by a constraint, for every
		// session, the owner's as well.
		owner := db.Raw(t)
		for name, check := range map[string]struct {
			role      *sql.DB
			statement string
		}{
			"a negative reservation":                    {owner, reservationOf(`amount = -1`)},
			"a reservation in no known status":          {owner, reservationOf(`status = 'forgotten'`)},
			"a reservation in no known budget mode":     {agent, newReservation("r-7", "1", "claim-r-7", "free", "held")},
			"a negative reservation made":               {agent, newReservation("r-3", "-1", "claim-r-3", "capped", "held")},
			"a reservation made with no claim key":      {agent, newReservation("r-4", "1", "", "capped", "held")},
			"usage that lowers the spending":            {agent, usage("-100", "0", "0")},
			"usage with a negative count of tokens in":  {agent, usage("0", "-1", "0")},
			"usage with a negative count of tokens out": {agent, usage("0", "0", "-1")},
		} {
			_, err := check.role.ExecContext(ctx, check.statement)
			var pg *pgconn.PgError
			if !errors.As(err, &pg) || pg.Code != "23514" {
				t.Errorf("the database must refuse %s by a constraint, got %v", name, err)
			}
		}
		for _, allowed := range []struct {
			name      string
			role      *sql.DB
			statement string
		}{
			{"a reservation the run is suspended on", control, reservationOf(`status = 'suspended'`)},
			{"a reservation the run gives up", control, reservationOf(`status = 'released'`)},
			{"usage the run reports", agent, usage("3", "10", "20")},
		} {
			if _, err := allowed.role.ExecContext(ctx, allowed.statement); err != nil {
				t.Errorf("the zone role must be allowed %s: %v", allowed.name, err)
			}
		}
		for name, statement := range map[string]string{
			"a released reservation held again": reservationOf(`status = 'held'`),
			"a released reservation settled":    reservationOf(`status = 'settled'`),
			"a released reservation suspended":  reservationOf(`status = 'suspended'`),
		} {
			refusedRow(t, control, name, statement)
		}
		if got := totals(t, agentStore, time.Unix(0, 0)); got.Reserved != 0 || got.SpentUnverified != 3 {
			t.Errorf("after the refused changes the budget counts %+v, want nothing reserved and 3 spent", got)
		}
	})
}

// rawStep takes a record step over a zone role's own connection the way the
// store does: the update of the record, which must change it, and the journal
// row of the step in one transaction, the row copied from the record as
// written.
func rawStep(t *testing.T, role *sql.DB, name string, ref core.IssueRef, set string, from core.State, event core.Event) {
	t.Helper()
	tx, err := role.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, recordUpdate(set, ref))
	if err != nil {
		t.Fatalf("%s was refused: %v", name, err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		t.Fatalf("%s changed %d records", name, changed)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO transitions (factory_id, issue, idempotency_key, from_state, to_state,
		event, version, lease_owner, fencing_token, attempt, run_id, actor_id, reason, evidence_refs, candidate_digest,
		release_digest, record_candidate_digest, record_release_digest, created_at)
		SELECT factory_id, issue, idempotency_key, $1, state, $2, version, lease_owner, fencing_token, attempt, run_id,
			'raw', '', '[]', '', '', candidate_digest, release_digest, updated_at
		FROM records WHERE factory_id = $3 AND issue = $4`,
		string(from), string(event), string(sqlstoretest.Factory), ref.String()); err != nil {
		t.Fatalf("the journal row of %s was refused: %v", name, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("%s with its journal row must commit: %v", name, err)
	}
}

// forgetStoreRules takes from the schema what the migration that holds the
// journal, the keys and the budget to the store's rules adds besides the row
// policies, for a test that restores the schema of an earlier migration.
func forgetStoreRules(t *testing.T, raw *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`DROP TRIGGER IF EXISTS sisf_record_journaled ON records`,
		`DROP FUNCTION IF EXISTS sisf_record_journaled()`,
		`DROP TRIGGER IF EXISTS sisf_reservation_run ON reservations`,
		`DROP FUNCTION IF EXISTS sisf_reservation_run()`,
		`ALTER TABLE reservations DROP CONSTRAINT IF EXISTS sisf_reservation_values`,
		`ALTER TABLE usage_ledger DROP CONSTRAINT IF EXISTS sisf_usage_values`,
	} {
		if _, err := raw.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

// A store whose roles an earlier release granted is held to the new rules
// from the moment it is migrated, keeping the roles its policies name.
func TestPostgresStoreRulesMigrationUpgradesAGrantedStore(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		raw := db.Raw(t)
		agentRole, controlRole := logins.Roles[core.ZoneAgent], logins.Roles[core.ZoneControl]
		// The schema as migration 11 left it: no journal pairing or budget
		// rules, a journal policy of each zone that does not bind a row to
		// its record, and the agent zone still submitting candidates.
		forgetStoreRules(t, raw)
		for _, statement := range []string{
			`DROP POLICY "sisf_agent_insert" ON transitions`,
			`CREATE POLICY "sisf_agent_insert" ON transitions FOR INSERT TO "` + agentRole + `"
				WITH CHECK (event IN ('candidate_submitted', 'claimed', 'development_started'))`,
			`DROP POLICY "sisf_control_insert" ON transitions`,
			`CREATE POLICY "sisf_control_insert" ON transitions FOR INSERT TO "` + controlRole + `" WITH CHECK (true)`,
			`DROP POLICY "sisf_agent_update" ON records`,
			`CREATE POLICY "sisf_agent_update" ON records FOR UPDATE TO "` + agentRole + `"
				USING (state IN ('Claimed', 'Developing', 'Ready'))
				WITH CHECK (state IN ('Claimed', 'Developing', 'QAEvaluation'))`,
			`DELETE FROM schema_migrations WHERE version > 11`,
			`GRANT UPDATE ON reservations TO "` + agentRole + `"`,
			`INSERT INTO reservations (factory_id, id, issue, run_id, budget_mode, amount, status, hard_cap_enforced,
				idempotency_key, created_at) VALUES ('` + string(sqlstoretest.Factory) + `', 'r-up', 'acme/app#7', 'run-1',
				'capped', 5, 'held', true, 'claim-r-up', 1)`,
		} {
			if _, err := raw.ExecContext(ctx, statement); err != nil {
				t.Fatalf("restore the schema of migration 11: %v", err)
			}
		}
		suspend := `UPDATE reservations SET status = 'suspended' WHERE factory_id = '` + string(sqlstoretest.Factory) + `' AND id = 'r-up'`
		// previousClaim is how the previous release's dispatcher makes the
		// reservation of a claim, as the agent zone.
		previousClaim := func(id string) string {
			return `INSERT INTO reservations
				(factory_id, id, issue, run_id, budget_mode, amount, status, hard_cap_enforced, idempotency_key, created_at)
				VALUES ('` + string(sqlstoretest.Factory) + `', '` + id + `', 'acme/app#8', 'run-1', 'capped', 5, 'held', true, 'claim-` + id + `', 1)
				ON CONFLICT (factory_id, id) DO UPDATE
				SET budget_mode = excluded.budget_mode, amount = excluded.amount, status = excluded.status,
				    hard_cap_enforced = excluded.hard_cap_enforced
				WHERE reservations.issue = excluded.issue AND reservations.run_id = excluded.run_id
				  AND reservations.idempotency_key = excluded.idempotency_key`
		}
		agent := rawLogin(t, logins.DSN[core.ZoneAgent])
		control := rawLogin(t, logins.DSN[core.ZoneControl])
		// The premise: the earlier schema lets forged rows in.
		for name, statement := range map[string]struct {
			role *sql.DB
			sql  string
		}{
			"a journal row for no record":              {agent, journalRow("acme/app#404", "Ready", "claimed", "Claimed", 1, "claim/run-1").sql()},
			"an intake step under the agent claim key": {control, journalRow("acme/app#405", "", "issue_received", "Intake", 1, "claim/run-1").sql()},
			"a reservation the agent role suspends":    {agent, suspend},
			"the previous release's claim reservation": {agent, previousClaim("r-before")},
			"a negative cost":                          {agent, `INSERT INTO usage_ledger (factory_id, issue, run_id, reservation_id, step, model_route, input_tokens, output_tokens, cost, verified, recorded_at) VALUES ('` + string(sqlstoretest.Factory) + `', 'acme/app#7', 'run-1', 'r-1', 'coding', '{}', 0, 0, -1, false, 1)`},
		} {
			if _, err := statement.role.ExecContext(ctx, statement.sql); err != nil {
				t.Fatalf("the earlier schema should let %s in, the premise of this test: %v", name, err)
			}
		}

		// The migration takes away what the previous release's dispatcher
		// needs, so it is not backward compatible: a store at migration 11
		// takes it only with the Owner's approval.
		migrator := db.Open(t, core.ZoneControl)
		wantCode(t, migrator.Migrate(ctx, state.MigrateOptions{}), core.CodeApprovalRequired)
		if version, err := migrator.SchemaVersion(ctx); err != nil || version != 11 {
			t.Fatalf("a store at migration 11 moved to %d without approval: %v", version, err)
		}
		if err := migrator.Migrate(ctx, state.MigrateOptions{AllowIncompatible: true}); err != nil {
			t.Fatalf("migrate a granted store at migration 11: %v", err)
		}
		// Even with nothing to conflict with, the previous release's claim
		// needs UPDATE on the reservations, which the agent role no longer
		// has.
		refusedRow(t, agent, "the previous release's claim reservation after the migration", previousClaim("r-after"))
		for policy, want := range map[string]string{"sisf_agent_insert": agentRole, "sisf_control_insert": controlRole} {
			var roles string
			if err := raw.QueryRowContext(ctx, `SELECT array_to_string(roles, ',') FROM pg_policies
				WHERE schemaname = current_schema() AND tablename = 'transitions' AND policyname = $1`, policy).Scan(&roles); err != nil {
				t.Fatalf("read the rewritten policy %s: %v", policy, err)
			}
			if roles != want {
				t.Errorf("the rewritten policy %s names %q, want %q", policy, roles, want)
			}
		}
		assertAgentJournalRules(t, db, agent)
		assertControlJournalRules(t, db, control)
		refusedRow(t, agent, "a reservation changed by the agent role after the migration", suspend)
		owner := db.Open(t, core.ZoneControl)
		claimed := claim(t, owner, readyRecord(t, owner, issue(61)), "worker-1", "run-1")
		refusedAtCommit(t, agent, "a record step with no journal row",
			recordUpdate(`state = 'Developing', version = version + 1, idempotency_key = 'develop/run-1'`, claimed.IssueID))
		_, err := agent.ExecContext(ctx, `INSERT INTO usage_ledger (factory_id, issue, run_id, reservation_id, step, model_route,
			input_tokens, output_tokens, cost, verified, recorded_at) VALUES ('`+string(sqlstoretest.Factory)+`', 'acme/app#7',
			'run-1', 'r-1', 'coding', '{}', 0, 0, -1, false, 1)`)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "23514" {
			t.Errorf("a negative cost after the migration must be refused by a constraint, got %v", err)
		}
	})
}
