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

// releaseCandidate drives an issue to ReleaseCandidate: run-1 of worker-1
// built a candidate and QA passed it.
func releaseCandidate(t *testing.T, s *sqlstore.Store, ref core.IssueRef) core.StateRecord {
	t.Helper()
	return along(t, s, developing(t, s, ref), core.EventCandidateSubmitted, core.EventQAPassed)
}

// A release the supervisor refused leaves its issue in ReleaseCandidate.
// The supervisor, which writes as the control zone, blocks such an issue
// under the issue lease and for the run the record names; a run never does,
// the application zone writes no step at all, and nobody does without the
// lease.
func TestOnlyTheControlZoneBlocksAnIssueWhoseReleaseWasRefused(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control := db.Open(t, core.ZoneControl)
		agent := db.Open(t, core.ZoneAgent)
		application := db.Open(t, core.ZoneApplication)
		candidate := releaseCandidate(t, control, issue(110))
		keyed := func(tr *core.Transition) { tr.IdempotencyKey = "release/run-1/release_refused" }
		refused(t, agent, "a refusal written by the agent zone", next(t, candidate, core.EventReleaseRefused, keyed), core.CodePermissionDenied)
		// The application zone may not even read the record.
		_, err := transition(application, next(t, candidate, core.EventReleaseRefused, keyed))
		wantCode(t, err, core.CodePermissionDenied)
		refused(t, control, "a refusal without the lease", next(t, candidate, core.EventReleaseRefused, func(tr *core.Transition) {
			keyed(tr)
			tr.LeaseOwner, tr.FencingToken = "", 0
		}), core.CodeStaleFencingToken)
		refused(t, control, "a refusal for another run", next(t, candidate, core.EventReleaseRefused, func(tr *core.Transition) {
			keyed(tr)
			tr.RunID = "run-2"
		}), core.CodeConflict)
		blocked := advance(t, control, candidate, core.EventReleaseRefused, keyed)
		if blocked.State != core.StateBlocked || blocked.RunID != candidate.RunID || blocked.Attempt != candidate.Attempt ||
			blocked.CandidateDigest != candidate.CandidateDigest || blocked.ReleaseDigest != "" {
			t.Fatalf("the refusal wrote %+v", blocked)
		}
		ready := advance(t, control, blocked, core.EventRetryAuthorized, func(tr *core.Transition) { tr.IdempotencyKey = "retry/after-refusal" })
		if ready.State != core.StateReady {
			t.Fatalf("the retry of a refused release left %+v", ready)
		}
	})
}

// The PostgreSQL script of the migration admits the new step to the
// control zone's policies only and installs the trigger that holds the
// leases to the writes of the lease API.
func TestTheReleaseRefusalMigrationAdmitsTheNewStepToTheControlZoneOnly(t *testing.T) {
	t.Parallel()
	shipped := sqlstore.Migrations()
	if len(shipped) < 10 || shipped[9].Version != 10 || !shipped[9].BackwardCompatible {
		t.Fatalf("migration 10 is %+v, want the backward compatible migration 10", shipped[9])
	}
	script, err := sqlstore.MigrationScript(10, sqlstore.DialectPostgres)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"('ReleaseCandidate', 'Blocked')", "'release_refused'", `"sisf_lease_step"`, "BEFORE INSERT OR UPDATE ON \"leases\""} {
		if !strings.Contains(script, want) {
			t.Errorf("the PostgreSQL script of migration 10 does not hold %s", want)
		}
	}
	agent := script[strings.Index(script, `"sisf_agent_insert"`):]
	if end := strings.Index(agent, "$sisf_migration$;"); end >= 0 {
		agent = agent[:end]
	}
	if strings.Contains(agent, "release_refused") {
		t.Errorf("migration 10 lets the agent zone journal a refused release:\n%s", agent)
	}
	sqlite, err := sqlstore.MigrationScript(10, sqlstore.DialectSQLite)
	if err != nil || !strings.Contains(sqlite, "SELECT 1;") {
		t.Fatalf("the SQLite script of migration 10 is %q, %v", sqlite, err)
	}
}

// restoreMigration9 puts a store back to the schema migration 9 left: the
// record trigger without the step out of a refused release, and no trigger
// on the leases.
func restoreMigration9(t *testing.T, raw *sql.DB) {
	t.Helper()
	var pairs []string
	for _, edge := range statemachine.Edges() {
		if edge.From != "" && edge.Event != core.EventReleaseRefused {
			pairs = append(pairs, "('"+string(edge.From)+"', '"+string(edge.To)+"')")
		}
	}
	if _, err := raw.ExecContext(ctx, `CREATE OR REPLACE FUNCTION sisf_record_step() RETURNS trigger
		LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $migration9$
		BEGIN
			IF pg_has_role(current_user, (SELECT relowner FROM pg_class WHERE oid = TG_RELID), 'USAGE')
				OR EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND rolbypassrls) THEN
				RETURN NEW;
			END IF;
			IF NEW.factory_id = OLD.factory_id AND NEW.issue = OLD.issue AND NEW.version = OLD.version + 1
				AND (OLD.state, NEW.state) IN (`+strings.Join(pairs, ", ")+`) THEN
				RETURN NEW;
			END IF;
			RAISE EXCEPTION 'not a step of migration 9' USING ERRCODE = 'insufficient_privilege';
		END
		$migration9$`); err != nil {
		t.Fatalf("restore the record trigger of migration 9: %v", err)
	}
	for _, statement := range []string{
		`DROP TRIGGER IF EXISTS sisf_lease_step ON leases`,
		`DROP FUNCTION IF EXISTS sisf_lease_step()`,
		`DELETE FROM schema_migrations WHERE version > 9`,
	} {
		if _, err := raw.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

// A store at migration 9 whose records stand in every state of the
// lifecycle keeps them as they are when it is migrated; afterwards the
// control zone's role blocks a refused release, the agent zone's role may
// not, and the agent zone's role can no longer raise a lease token with
// plain SQL.
func TestPostgresReleaseRefusalMigrationUpgradesAStoreWithRecordsInEveryState(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		raw := db.Raw(t)
		owner := db.Open(t, core.ZoneControl)

		intake := mustTransition(t, owner, received(issue(120)))
		clarifying := advance(t, owner, mustTransition(t, owner, received(issue(121))), core.EventInfoMissing, nil)
		scoping := advance(t, owner, mustTransition(t, owner, received(issue(122))), core.EventInfoSufficient, nil)
		pending := along(t, owner, mustTransition(t, owner, received(issue(123))), core.EventInfoSufficient, core.EventApprovalRequired)
		ready := readyRecord(t, owner, issue(124))
		claimed := claim(t, owner, readyRecord(t, owner, issue(125)), "worker-1", "run-1")
		dev := developing(t, owner, issue(126))
		evaluating := along(t, owner, developing(t, owner, issue(127)), core.EventCandidateSubmitted)
		candidate := releaseCandidate(t, owner, issue(128))
		canary := along(t, owner, releaseCandidate(t, owner, issue(129)), core.EventRolloutStarted)
		monitoring := along(t, owner, releaseCandidate(t, owner, issue(130)), core.EventRolloutStarted, core.EventRolloutHealthy)
		verified := along(t, owner, releaseCandidate(t, owner, issue(134)), core.EventRolloutStarted, core.EventRolloutHealthy,
			core.EventPostDeployVerified)
		closed := along(t, owner, releaseCandidate(t, owner, issue(131)), core.EventRolloutStarted, core.EventRolloutHealthy,
			core.EventPostDeployVerified, core.EventIssueClosed)
		rolledBack := along(t, owner, releaseCandidate(t, owner, issue(132)), core.EventRolloutStarted, core.EventErrorThresholdExceeded)
		blocked := along(t, owner, releaseCandidate(t, owner, issue(133)), core.EventRolloutStarted, core.EventErrorThresholdExceeded,
			core.EventRecoveryReported)
		before := []core.StateRecord{intake, clarifying, scoping, pending, ready, claimed, dev, evaluating, candidate, canary,
			monitoring, verified, closed, rolledBack, blocked}
		states := map[core.State]bool{}
		for _, rec := range before {
			states[rec.State] = true
		}
		if len(states) != len(core.States()) {
			t.Fatalf("the records stand in %d states, want all %d", len(states), len(core.States()))
		}

		restoreMigration9(t, raw)
		controlRole := rawLogin(t, logins.DSN[core.ZoneControl])
		block := recordUpdate(`state = 'Blocked', version = version + 1`, candidate.IssueID)
		if _, err := controlRole.ExecContext(ctx, block); err == nil {
			t.Fatal("the trigger of migration 9 should refuse the new step, the premise of this test")
		}

		migrator := db.Open(t, core.ZoneControl)
		if err := migrator.Migrate(ctx, state.MigrateOptions{}); err != nil {
			t.Fatalf("migrate a store at migration 9: %v", err)
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
		// The agent zone's policy does not let its role see a record in
		// ReleaseCandidate to change it, so the update changes nothing.
		if result, err := agentRole.ExecContext(ctx, block); err != nil {
			refusedRow(t, agentRole, "a refused release blocked by the agent role", block)
		} else if changed, _ := result.RowsAffected(); changed != 0 {
			t.Fatalf("the agent role blocked a refused release: %d records changed", changed)
		}
		refusedRow(t, agentRole, "a lease token raised by the agent role",
			`UPDATE leases SET token = token + 7 WHERE resource = '`+state.IssueLeaseResource(candidate.IssueID)+`'`)
		control := db.OpenWith(t, logins.DSN[core.ZoneControl], core.ZoneControl)
		db.Clock.Advance(2 * leaseTTL)
		lease := leaseOn(t, control, candidate.IssueID, "supervisor")
		refusal := advance(t, control, candidate, core.EventReleaseRefused, func(tr *core.Transition) {
			under(lease)(tr)
			tr.IdempotencyKey = "release/run-1/release_refused"
		})
		if refusal.State != core.StateBlocked {
			t.Fatalf("the control role's refusal after the migration wrote %+v", refusal)
		}
	})
}
