package sqlstore_test

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// Each zone's store works over its own role's connection: what the database
// grants and the row policies it enforces leave every legitimate step of
// the zone possible.
func TestEachZoneWorksOverItsOwnRole(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		control := db.OpenWith(t, logins.DSN[core.ZoneControl], core.ZoneControl)
		agent := db.OpenWith(t, logins.DSN[core.ZoneAgent], core.ZoneAgent)

		seedControlDocuments(t, control)
		mustUpdate(t, control, func(tx state.Tx) error {
			if _, err := tx.AcceptDelivery(ctx, state.InboxEntry{Source: "github", DeliveryID: "d-1"}); err != nil {
				return err
			}
			if err := tx.Enqueue(ctx, state.OutboxMessage{Topic: "t", Key: "k"}); err != nil {
				return err
			}
			if err := tx.MarkDeliveryProcessed(ctx, "github", "d-1"); err != nil {
				return err
			}
			_, err := tx.PutKV(ctx, state.KVEntry{Namespace: "approvals", Key: "request/apr-1", Value: []byte(`{}`), Version: 1})
			return err
		})
		if _, err := control.AppendAuditRow(ctx, func(int64, string) (string, []byte, error) { return "h", []byte("{}"), nil }); err != nil {
			t.Fatalf("append audit as the control role: %v", err)
		}
		ready := readyRecord(t, control, issue(3))

		// The run, as the dispatcher drives it.
		claimed := claim(t, agent, ready, "worker-1", "run-1")
		evaluating := along(t, agent, claimed, core.EventDevelopmentStarted, core.EventCandidateSubmitted)
		// The verdict of QA is the control zone's, over its own role.
		evaluating = advance(t, control, evaluating, core.EventQAFailed, nil)
		mustUpdate(t, agent, func(tx state.Tx) error {
			if _, err := tx.GetKV(ctx, "install", "budget_confirmation"); err != nil {
				return err
			}
			if _, err := tx.ListKV(ctx, "intake", "", 0); err != nil {
				return err
			}
			if _, err := tx.GetKV(ctx, "control.policy", "refs/abc"); err != nil {
				return err
			}
			written, err := tx.PutKV(ctx, state.KVEntry{Namespace: "dispatcher", Key: "run/run-1", Value: []byte(`{}`)})
			if err != nil {
				return err
			}
			if _, err := tx.PutKV(ctx, state.KVEntry{Namespace: "dispatcher", Key: "run/run-1", Value: []byte(`{"done":true}`), Version: written.Version}); err != nil {
				return err
			}
			if err := tx.PutReservation(ctx, reservation("r-1", 1)); err != nil {
				return err
			}
			if _, err := tx.GetReservation(ctx, "r-1"); err != nil {
				return err
			}
			if err := tx.AppendUsage(ctx, spend(1, true, db.Clock.Now())); err != nil {
				return err
			}
			return tx.ReleaseLease(ctx, state.Lease{Resource: state.IssueLeaseResource(issue(3)), Owner: "worker-1", Token: evaluating.FencingToken})
		})

		// The control zone sees the run state and takes the run on.
		if got := documentValue(t, control, "dispatcher", "run/run-1"); string(got.Value) != `{"done":true}` {
			t.Fatalf("the control role reads the run record as %q", got.Value)
		}
		if got := record(t, control, issue(3)); got.State != core.StateDeveloping || got.Version != evaluating.Version {
			t.Fatalf("the control role reads the record as %+v", got)
		}
	})
}

// A stolen agent-zone credential used with plain SQL, past the store, is
// held by the database to what the agent zone may do.
func TestPostgresRefusesTheAgentRoleWhatItsZoneMayNotDo(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		owner := db.Open(t, core.ZoneControl)
		agentStore := db.OpenWith(t, logins.DSN[core.ZoneAgent], core.ZoneAgent)
		seedControlDocuments(t, owner)
		mustUpdate(t, agentStore, func(tx state.Tx) error {
			_, err := tx.PutKV(ctx, state.KVEntry{Namespace: "dispatcher", Key: "run/run-1", Value: []byte(`{}`)})
			return err
		})
		ref := issue(1)
		mustTransition(t, owner, received(ref))
		mustTransition(t, owner, step(ref, core.StateIntake, core.EventInfoSufficient, core.StateScopeDraft, 1, "scoped"))
		mustTransition(t, owner, step(ref, core.StateScopeDraft, core.EventApprovalRequired, core.StateApprovalPending, 2, "asked"))
		developing(t, owner, issue(2))

		agent, err := sql.Open("pgx", logins.DSN[core.ZoneAgent])
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = agent.Close() })
		factory := string(sqlstoretest.Factory)

		// Refused outright, for want of a privilege or by a row policy.
		for _, statement := range []string{
			`INSERT INTO kv (factory_id, namespace, entry_key, entry_value, version, updated_at) VALUES ('` + factory + `', 'policy', 'baseline', '\x7b7d', 1, 0)`,
			`INSERT INTO kv (factory_id, namespace, entry_key, entry_value, version, updated_at) VALUES ('` + factory + `', 'install', 'budget_confirmation', '\x7b7d', 1, 0)`,
			`INSERT INTO kv (factory_id, namespace, entry_key, entry_value, version, updated_at) VALUES ('` + factory + `', 'approvals', 'decision/apr-1', '\x7b7d', 1, 0)`,
			`INSERT INTO kv (factory_id, namespace, entry_key, entry_value, version, updated_at) VALUES ('` + factory + `', 'intake', 'issue/x', '\x7b7d', 1, 0)`,
			`UPDATE kv SET namespace = 'install', entry_key = 'budget_confirmation' WHERE namespace = 'dispatcher'`,
			`DELETE FROM kv`,
			`INSERT INTO transitions (factory_id, issue, idempotency_key, from_state, to_state, event, version, lease_owner, fencing_token, attempt, run_id, actor_id, reason, evidence_refs, candidate_digest, release_digest, created_at)
				VALUES ('` + factory + `', 'acme/app#1', 'forged', 'ApprovalPending', 'Ready', 'owner_approved', 4, '', 0, 0, '', 'owner', '', '[]', '', '', 0)`,
			`UPDATE records SET state = 'Ready' WHERE issue = 'acme/app#2'`,
			`UPDATE records SET state = 'ReleaseCandidate' WHERE issue = 'acme/app#2'`,
			`INSERT INTO records (factory_id, issue, state, version, idempotency_key, lease_owner, fencing_token, attempt, run_id, updated_at)
				VALUES ('` + factory + `', 'acme/app#9', 'Ready', 1, 'forged', '', 0, 0, '', 0)`,
			`DELETE FROM records`,
			`SELECT count(*) FROM inbox`,
			`SELECT count(*) FROM outbox`,
			`SELECT count(*) FROM audit_log`,
			`INSERT INTO audit_head (factory_id, seq, hash) VALUES ('forged', 0, '')`,
		} {
			_, err := agent.ExecContext(ctx, statement)
			var pg *pgconn.PgError
			if !errors.As(err, &pg) || pg.Code != "42501" {
				t.Errorf("the agent role must be refused %q, got %v", statement, err)
			}
		}

		// Rows of other zones are out of the agent role's sight: it reads
		// none of them and changes none of them.
		for _, statement := range []string{
			`UPDATE kv SET entry_value = '\x7b7d' WHERE namespace IN ('policy', 'approvals', 'install', 'intake', 'control.policy')`,
			`UPDATE records SET state = 'Ready' WHERE issue = 'acme/app#1'`,
		} {
			result, err := agent.ExecContext(ctx, statement)
			if err != nil {
				t.Errorf("%q: %v", statement, err)
				continue
			}
			if changed, _ := result.RowsAffected(); changed != 0 {
				t.Errorf("the agent role changed %d rows with %q", changed, statement)
			}
		}
		for namespace, visible := range map[string]int{
			"approvals": 0, "policy": 0, "control.issue_view": 0, "install": 1, "intake": 1, "control.policy": 1, "dispatcher": 1,
		} {
			var n int
			if err := agent.QueryRowContext(ctx, `SELECT count(*) FROM kv WHERE namespace = $1`, namespace).Scan(&n); err != nil {
				t.Fatalf("count %s documents as the agent role: %v", namespace, err)
			}
			if n != visible {
				t.Errorf("the agent role sees %d %s documents, want %d", n, namespace, visible)
			}
		}

		// Nothing the agent role tried took effect.
		for _, doc := range controlDocuments {
			if got := documentValue(t, owner, doc.Namespace, doc.Key); string(got.Value) != string(doc.Value) || got.Version != 1 {
				t.Errorf("%s/%s after the refused writes = %q", doc.Namespace, doc.Key, got.Value)
			}
		}
		if got := record(t, owner, ref); got.State != core.StateApprovalPending {
			t.Errorf("the issue awaiting approval is now %s", got.State)
		}
		if got := record(t, owner, issue(2)); got.State != core.StateDeveloping {
			t.Errorf("the issue in development is now %s", got.State)
		}
	})
}

// The application zone's role reaches nothing of the factory's state over
// its own connection either.
func TestPostgresRefusesTheApplicationRoleTheFactoryState(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		application, err := sql.Open("pgx", logins.DSN[core.ZoneApplication])
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = application.Close() })
		for _, table := range []string{"records", "transitions", "kv", "leases", "reservations", "usage_ledger"} {
			_, err := application.ExecContext(ctx, `SELECT count(*) FROM `+table)
			var pg *pgconn.PgError
			if !errors.As(err, &pg) || pg.Code != "42501" {
				t.Errorf("the application role must not read %s, got %v", table, err)
			}
		}
		if _, err := application.ExecContext(ctx, `SELECT version FROM schema_migrations`); err != nil {
			t.Errorf("the application role cannot read the schema version: %v", err)
		}
	})
}
