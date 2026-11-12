package sqlstore_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// verifiedRecord drives an issue to Verified: the release of run-1 passed
// its observation window.
func verifiedRecord(t *testing.T, s *sqlstore.Store, ref core.IssueRef) core.StateRecord {
	t.Helper()
	return along(t, s, releaseCandidate(t, s, ref), core.EventRolloutStarted, core.EventRolloutHealthy, core.EventPostDeployVerified)
}

// A verified release that can no longer be judged blocks its issue. The
// supervisor, which writes as the control zone, takes the step for the run
// the record names; a run never does and the application zone writes no
// step at all. The Owner's retry takes the issue back to Ready.
func TestOnlyTheControlZoneBlocksAnIssueWhoseVerifiedReleaseCannotBeJudged(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control := db.Open(t, core.ZoneControl)
		agent := db.Open(t, core.ZoneAgent)
		application := db.Open(t, core.ZoneApplication)
		verified := verifiedRecord(t, control, issue(140))
		keyed := func(tr *core.Transition) { tr.IdempotencyKey = "release/run-1/verified_release_unjudged" }
		refused(t, agent, "a block written by the agent zone", next(t, verified, core.EventVerifiedReleaseUnjudged, keyed), core.CodePermissionDenied)
		_, err := transition(application, next(t, verified, core.EventVerifiedReleaseUnjudged, keyed))
		wantCode(t, err, core.CodePermissionDenied)
		refused(t, control, "a block for another run", next(t, verified, core.EventVerifiedReleaseUnjudged, func(tr *core.Transition) {
			keyed(tr)
			tr.RunID = "run-2"
		}), core.CodeConflict)
		blocked := advance(t, control, verified, core.EventVerifiedReleaseUnjudged, keyed)
		if blocked.State != core.StateBlocked || blocked.RunID != verified.RunID || blocked.ReleaseDigest != verified.ReleaseDigest {
			t.Fatalf("the block wrote %+v", blocked)
		}
		ready := advance(t, control, blocked, core.EventRetryAuthorized, func(tr *core.Transition) { tr.IdempotencyKey = "retry/after-unjudged" })
		if ready.State != core.StateReady {
			t.Fatalf("the retry of an unjudged verified release left %+v", ready)
		}
	})
}

// The PostgreSQL script of migration 11 admits the new step to the control
// zone's policies only; on SQLite it changes nothing.
func TestTheVerifiedReleaseUnjudgedMigrationAdmitsTheNewStepToTheControlZoneOnly(t *testing.T) {
	t.Parallel()
	shipped := sqlstore.Migrations()
	if len(shipped) < 11 || shipped[10].Version != 11 || !shipped[10].BackwardCompatible {
		t.Fatalf("the migrations are %+v, want the backward compatible migration 11", shipped)
	}
	script, err := sqlstore.MigrationScript(11, sqlstore.DialectPostgres)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"('Verified', 'Blocked')", "'verified_release_unjudged'"} {
		if !strings.Contains(script, want) {
			t.Errorf("the PostgreSQL script of migration 11 does not hold %s", want)
		}
	}
	for _, zone := range []string{`"sisf_agent_insert"`, `"sisf_application_insert"`} {
		at := strings.Index(script, zone)
		if at < 0 {
			continue
		}
		policy := script[at:]
		if end := strings.Index(policy, "$sisf_migration$;"); end >= 0 {
			policy = policy[:end]
		}
		if strings.Contains(policy, "verified_release_unjudged") {
			t.Errorf("migration 11 lets %s journal the block of a verified release:\n%s", zone, policy)
		}
	}
	sqlite, err := sqlstore.MigrationScript(11, sqlstore.DialectSQLite)
	if err != nil || !strings.Contains(sqlite, "SELECT 1;") {
		t.Fatalf("the SQLite script of migration 11 is %q, %v", sqlite, err)
	}
}

// restoreMigration10 puts a store back to the schema migration 10 left: the
// record trigger without the step out of Verified into Blocked.
func restoreMigration10(t *testing.T, raw *sql.DB) {
	t.Helper()
	var pairs []string
	for _, edge := range statemachine.Edges() {
		if edge.From != "" && edge.Event != core.EventVerifiedReleaseUnjudged {
			pairs = append(pairs, "('"+string(edge.From)+"', '"+string(edge.To)+"')")
		}
	}
	if _, err := raw.ExecContext(ctx, `CREATE OR REPLACE FUNCTION sisf_record_step() RETURNS trigger
		LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $migration10$
		BEGIN
			IF pg_has_role(current_user, (SELECT relowner FROM pg_class WHERE oid = TG_RELID), 'USAGE')
				OR EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND rolbypassrls) THEN
				RETURN NEW;
			END IF;
			IF NEW.factory_id = OLD.factory_id AND NEW.issue = OLD.issue AND NEW.version = OLD.version + 1
				AND (OLD.state, NEW.state) IN (`+strings.Join(pairs, ", ")+`) THEN
				RETURN NEW;
			END IF;
			RAISE EXCEPTION 'not a step of migration 10' USING ERRCODE = 'insufficient_privilege';
		END
		$migration10$`); err != nil {
		t.Fatalf("restore the record trigger of migration 10: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version > 10`); err != nil {
		t.Fatalf("forget migration 11: %v", err)
	}
}

// A store at migration 10 whose records stand in every state of the
// lifecycle keeps them as they are when it is migrated to 11; afterwards
// the control zone's role blocks a verified release that cannot be judged,
// and the agent zone's role may not.
func TestPostgresVerifiedReleaseUnjudgedMigrationUpgradesAStoreWithRecordsInEveryState(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		raw := db.Raw(t)
		owner := db.Open(t, core.ZoneControl)

		intake := mustTransition(t, owner, received(issue(150)))
		clarifying := advance(t, owner, mustTransition(t, owner, received(issue(151))), core.EventInfoMissing, nil)
		scoping := advance(t, owner, mustTransition(t, owner, received(issue(152))), core.EventInfoSufficient, nil)
		pending := along(t, owner, mustTransition(t, owner, received(issue(153))), core.EventInfoSufficient, core.EventApprovalRequired)
		ready := readyRecord(t, owner, issue(154))
		claimed := claim(t, owner, readyRecord(t, owner, issue(155)), "worker-1", "run-1")
		dev := developing(t, owner, issue(156))
		evaluating := along(t, owner, developing(t, owner, issue(157)), core.EventCandidateSubmitted)
		candidate := releaseCandidate(t, owner, issue(158))
		canary := along(t, owner, releaseCandidate(t, owner, issue(159)), core.EventRolloutStarted)
		monitoring := along(t, owner, releaseCandidate(t, owner, issue(160)), core.EventRolloutStarted, core.EventRolloutHealthy)
		verified := verifiedRecord(t, owner, issue(161))
		closed := along(t, owner, verifiedRecord(t, owner, issue(162)), core.EventIssueClosed)
		rolledBack := along(t, owner, releaseCandidate(t, owner, issue(163)), core.EventRolloutStarted, core.EventErrorThresholdExceeded)
		blocked := along(t, owner, releaseCandidate(t, owner, issue(164)), core.EventReleaseRefused)
		before := []core.StateRecord{intake, clarifying, scoping, pending, ready, claimed, dev, evaluating, candidate, canary,
			monitoring, verified, closed, rolledBack, blocked}
		states := map[core.State]bool{}
		for _, rec := range before {
			states[rec.State] = true
		}
		if len(states) != len(core.States()) {
			t.Fatalf("the records stand in %d states, want all %d", len(states), len(core.States()))
		}

		restoreMigration10(t, raw)
		controlRole := rawLogin(t, logins.DSN[core.ZoneControl])
		block := recordUpdate(`state = 'Blocked', version = version + 1`, verified.IssueID)
		if _, err := controlRole.ExecContext(ctx, block); err == nil {
			t.Fatal("the trigger of migration 10 should refuse the new step, the premise of this test")
		}

		migrator := db.Open(t, core.ZoneControl)
		if err := migrator.Migrate(ctx, state.MigrateOptions{}); err != nil {
			t.Fatalf("migrate a store at migration 10: %v", err)
		}
		if version, err := migrator.SchemaVersion(ctx); err != nil || version != len(sqlstore.Migrations()) {
			t.Fatalf("the store is at version %d after the migration, %v", version, err)
		}
		for _, rec := range before {
			if got := record(t, migrator, rec.IssueID); got != rec {
				t.Fatalf("the migration changed %s from %+v to %+v", rec.IssueID, rec, got)
			}
		}

		agentRole := rawLogin(t, logins.DSN[core.ZoneAgent])
		// The agent zone's policy does not let its role see a Verified
		// record to change it, so the update changes nothing.
		if result, err := agentRole.ExecContext(ctx, block); err != nil {
			refusedRow(t, agentRole, "a verified release blocked by the agent role", block)
		} else if changed, _ := result.RowsAffected(); changed != 0 {
			t.Fatalf("the agent role blocked a verified release: %d records changed", changed)
		}
		control := db.OpenWith(t, logins.DSN[core.ZoneControl], core.ZoneControl)
		db.Clock.Advance(2 * leaseTTL)
		lease := leaseOn(t, control, verified.IssueID, "supervisor")
		unjudged := advance(t, control, verified, core.EventVerifiedReleaseUnjudged, func(tr *core.Transition) {
			under(lease)(tr)
			tr.IdempotencyKey = "release/run-1/verified_release_unjudged"
		})
		if unjudged.State != core.StateBlocked {
			t.Fatalf("the control role's block after the migration wrote %+v", unjudged)
		}
	})
}
