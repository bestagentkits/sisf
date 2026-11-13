package sqlstore_test

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// A run that stopped without a candidate leaves its issue in Claimed or in
// Developing. The control zone blocks such an issue, under the issue lease
// and for the run the record names; the run itself never does, and nobody
// does without the lease.
func TestOnlyTheControlZoneBlocksAnIssueWhoseRunStopped(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control := db.Open(t, core.ZoneControl)
		agent := db.Open(t, core.ZoneAgent)
		for i, stopped := range []struct {
			event core.Event
			reach []core.Event
		}{
			{core.EventClaimStopped, nil},
			{core.EventDevelopmentStopped, []core.Event{core.EventDevelopmentStarted}},
		} {
			held := along(t, agent, claim(t, agent, readyRecord(t, control, issue(60+i)), "worker-1", "run-1"), stopped.reach...)
			keyed := func(tr *core.Transition) { tr.IdempotencyKey = "stop/run-1" }
			refused(t, agent, string(stopped.event)+" written by the agent zone", next(t, held, stopped.event, keyed), core.CodePermissionDenied)
			refused(t, control, string(stopped.event)+" without the lease", next(t, held, stopped.event, func(tr *core.Transition) {
				keyed(tr)
				tr.LeaseOwner, tr.FencingToken = "", 0
			}), core.CodeStaleFencingToken)
			refused(t, control, string(stopped.event)+" for another run", next(t, held, stopped.event, func(tr *core.Transition) {
				keyed(tr)
				tr.RunID = "run-2"
			}), core.CodeConflict)
			blocked := advance(t, control, held, stopped.event, keyed)
			if blocked.State != core.StateBlocked || blocked.RunID != held.RunID || blocked.Attempt != held.Attempt {
				t.Fatalf("%s wrote %+v", stopped.event, blocked)
			}
		}
	})
}

// A store migrated by an earlier release holds its records to the steps of
// that release. The migration that adds the steps out of a stopped run
// writes the trigger and the zones' policies afresh, so the control zone's
// role may take them and the agent zone's still may not.
func TestTheRunStopMigrationAdmitsTheNewStepsToTheControlZoneOnly(t *testing.T) {
	t.Parallel()
	shipped := sqlstore.Migrations()
	if len(shipped) < 8 || shipped[7].Version != 8 || !shipped[7].BackwardCompatible {
		t.Fatalf("the shipped migrations are %+v, want a backward compatible migration 8", shipped)
	}
	script, err := sqlstore.MigrationScript(8, sqlstore.DialectPostgres)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"('Claimed', 'Blocked')", "('Developing', 'Blocked')", "'claim_stopped'", "'development_stopped'"} {
		if !strings.Contains(script, want) {
			t.Errorf("the PostgreSQL script of migration 8 does not hold %s", want)
		}
	}
	agent := script[strings.Index(script, `"sisf_agent_insert"`):]
	if end := strings.Index(agent, "$sisf_migration$;"); end >= 0 {
		agent = agent[:end]
	}
	if strings.Contains(agent, "_stopped") {
		t.Errorf("migration 8 lets the agent zone journal a stopped run:\n%s", agent)
	}
}

// The migration holds a store whose roles an earlier release granted to
// the steps of this release, keeping the roles its policies name.
func TestPostgresRunStopMigrationUpgradesAGrantedStore(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		raw := db.Raw(t)
		control := db.Open(t, core.ZoneControl)
		held := claim(t, control, readyRecord(t, control, issue(71)), "worker-1", "run-1")
		// The trigger as the earlier release left it: no step from Claimed
		// to Blocked.
		if _, err := raw.ExecContext(ctx, `CREATE OR REPLACE FUNCTION sisf_record_step() RETURNS trigger
			LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $earlier$
			BEGIN
				IF pg_has_role(current_user, (SELECT relowner FROM pg_class WHERE oid = TG_RELID), 'USAGE') THEN
					RETURN NEW;
				END IF;
				IF (OLD.state, NEW.state) IN (('Claimed', 'Developing')) AND NEW.version = OLD.version + 1 THEN
					RETURN NEW;
				END IF;
				RAISE EXCEPTION 'not a step of the earlier release' USING ERRCODE = 'insufficient_privilege';
			END
			$earlier$`); err != nil {
			t.Fatalf("restore the earlier trigger: %v", err)
		}
		controlRole := rawLogin(t, logins.DSN[core.ZoneControl])
		block := `UPDATE records SET state = 'Blocked', version = version + 1 WHERE issue = $1`
		if _, err := controlRole.ExecContext(ctx, block, held.IssueID.String()); err == nil {
			t.Fatal("the earlier trigger should refuse the new step, the premise of this test")
		}
		script, err := sqlstore.MigrationScript(8, sqlstore.DialectPostgres)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.ExecContext(ctx, script); err != nil {
			t.Fatalf("apply the migration to a granted store: %v", err)
		}
		refusedRow(t, rawLogin(t, logins.DSN[core.ZoneAgent]), "a stopped run blocked by the agent role",
			`UPDATE records SET state = 'Blocked', version = version + 1 WHERE issue = '`+held.IssueID.String()+`'`)
		rawStep(t, controlRole, "the control role's new step after the migration", held.IssueID,
			`state = 'Blocked', version = version + 1, idempotency_key = 'stop/run-1'`, core.StateClaimed, core.EventClaimStopped)
	})
}
