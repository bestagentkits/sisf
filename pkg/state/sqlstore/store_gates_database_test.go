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

// The same rule holds the control role used with plain SQL.
func TestPostgresHoldsTheControlRoleOffTheAgentKeys(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		control := rawLogin(t, logins.DSN[core.ZoneControl])
		received := func(key string) string {
			return journalRow("acme/app#41", "", "issue_received", "Intake", 1, key).sql()
		}
		refusedRow(t, control, "an intake step under the agent zone's claim key", received("claim/run-1"))
		refusedRow(t, control, "an intake step under the agent zone's development key", received("develop/run-1"))
		if _, err := control.ExecContext(ctx, received("intake/acme/app#41/issue_received")); err != nil {
			t.Fatalf("the control role must journal a step under its own key: %v", err)
		}
	})
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
		refusedRow(t, agent, "a record step with no journal row",
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

// A reservation stays the money of the run it was made for, and neither a
// reservation nor metered usage can lower what the budget counts.
func TestPostgresHoldsTheAgentRoleToTheReservationsAndUsageItMayWrite(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		agentStore := db.OpenWith(t, logins.DSN[core.ZoneAgent], core.ZoneAgent)
		mustUpdate(t, agentStore, func(tx state.Tx) error { return tx.PutReservation(ctx, reservation("r-1", 500)) })
		agent := rawLogin(t, logins.DSN[core.ZoneAgent])
		reservationOf := func(set string) string {
			return `UPDATE reservations SET ` + set + ` WHERE factory_id = '` + string(sqlstoretest.Factory) + `' AND id = 'r-1'`
		}
		usage := func(cost, input, output string) string {
			return `INSERT INTO usage_ledger (factory_id, issue, run_id, reservation_id, step, model_route,
				input_tokens, output_tokens, cost, verified, recorded_at) VALUES ('` + string(sqlstoretest.Factory) +
				`', 'acme/app#7', 'run-1', 'r-1', 'coding', '{}', ` + input + `, ` + output + `, ` + cost + `, false, 1)`
		}
		// A reservation that changes hands is refused as a privilege the
		// role does not have.
		for name, statement := range map[string]string{
			"a reservation handed to another run":    reservationOf(`run_id = 'run-9'`),
			"a reservation handed to another issue":  reservationOf(`issue = 'acme/app#9'`),
			"a reservation handed to another claim":  reservationOf(`idempotency_key = 'claim-other'`),
			"a reservation moved to another factory": reservationOf(`factory_id = 'other'`),
			"a reservation renamed":                  reservationOf(`id = 'r-2'`),
			"a reservation backdated":                reservationOf(`created_at = 0`),
		} {
			refusedRow(t, agent, name, statement)
		}
		// A value no store writes is refused by a constraint, for every
		// session.
		newReservation := func(id, amount, key string) string {
			return `INSERT INTO reservations (factory_id, id, issue, run_id, budget_mode, amount, status, hard_cap_enforced,
				idempotency_key, created_at) VALUES ('` + string(sqlstoretest.Factory) + `', '` + id + `', 'acme/app#7', 'run-1',
				'capped', ` + amount + `, 'held', true, '` + key + `', 1)`
		}
		for name, statement := range map[string]string{
			"a negative reservation":                    reservationOf(`amount = -1`),
			"a reservation in no known status":          reservationOf(`status = 'forgotten'`),
			"a reservation in no known budget mode":     reservationOf(`budget_mode = 'free'`),
			"a negative reservation made":               newReservation("r-3", "-1", "claim-r-3"),
			"a reservation made with no claim key":      newReservation("r-4", "1", ""),
			"usage that lowers the spending":            usage("-100", "0", "0"),
			"usage with a negative count of tokens in":  usage("0", "-1", "0"),
			"usage with a negative count of tokens out": usage("0", "0", "-1"),
		} {
			_, err := agent.ExecContext(ctx, statement)
			var pg *pgconn.PgError
			if !errors.As(err, &pg) || pg.Code != "23514" {
				t.Errorf("the database must refuse %s by a constraint, got %v", name, err)
			}
		}
		for name, statement := range map[string]string{
			"a reservation the run gives up": reservationOf(`status = 'released'`),
			"usage the run reports":          usage("3", "10", "20"),
		} {
			if _, err := agent.ExecContext(ctx, statement); err != nil {
				t.Errorf("the agent role must be allowed %s: %v", name, err)
			}
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
