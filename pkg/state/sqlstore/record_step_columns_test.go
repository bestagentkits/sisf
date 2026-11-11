package sqlstore_test

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// recordUpdate is an UPDATE of one record of the test factory, as a stolen
// zone credential would write it with plain SQL.
func recordUpdate(set string, ref core.IssueRef) string {
	return `UPDATE records SET ` + set + ` WHERE factory_id = '` + string(sqlstoretest.Factory) + `' AND issue = '` + ref.String() + `'`
}

// changedOne fails the test unless statement, run over db, changed exactly
// one record.
func changedOne(t *testing.T, db *sql.DB, name, statement string) {
	t.Helper()
	result, err := db.ExecContext(ctx, statement)
	if err != nil {
		t.Fatalf("%s was refused: %v", name, err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		t.Fatalf("%s changed %d records", name, changed)
	}
}

// developingAtAttempt2 drives an issue through one failed evaluation: run-1
// of worker-1 develops it again, on its second attempt.
func developingAtAttempt2(t *testing.T, s *sqlstore.Store, ref core.IssueRef) core.StateRecord {
	t.Helper()
	evaluating := along(t, s, developing(t, s, ref), core.EventCandidateSubmitted)
	again := advance(t, s, evaluating, core.EventQAFailed, nil)
	if again.State != core.StateDeveloping || again.Attempt != 2 {
		t.Fatalf("the failed evaluation wrote %+v", again)
	}
	return again
}

// A stolen agent-zone credential used with plain SQL takes a step of its
// own only as the store writes it: it cannot reset or skip the attempt count
// the attempt limit is judged against, bring back a run the issue had
// before, swap the run, the candidate or the release a step is bound to, or
// write under a fencing token that is not the issue lease's latest.
func TestPostgresRefusesAStepThatRewritesWhatTheRecordIsAbout(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		agent := rawLogin(t, logins.DSN[core.ZoneAgent])
		owner := db.Open(t, core.ZoneControl)

		claimed := claim(t, owner, readyRecord(t, owner, issue(81)), "worker-1", "run-1")
		retrying := developingAtAttempt2(t, owner, issue(82))
		ready := rolledBackAndRetried(t, owner, claim(t, owner, readyRecord(t, owner, issue(83)), "worker-1", "run-1"), "first")
		ready = rolledBackAndRetried(t, owner, claim(t, owner, ready, "worker-1", "run-2"), "second")
		lease := leaseOn(t, owner, ready.IssueID, "worker-1")
		releasing := along(t, owner, developing(t, owner, issue(84)), core.EventCandidateSubmitted, core.EventQAPassed)

		toDeveloping := `state = 'Developing', version = version + 1`
		toEvaluation := `state = 'QAEvaluation', version = version + 1, candidate_digest = '` + string(otherCandidate) + `'`
		// toClaimed is the claim of run, at attempt, naming candidate, under
		// token of the lease worker-1 holds.
		toClaimed := func(run core.RunID, attempt int, candidate core.Digest, token core.FencingToken) string {
			return fmt.Sprintf(`state = 'Claimed', version = version + 1, idempotency_key = 'claim/again', run_id = '%s',
				attempt = %d, candidate_digest = '%s', release_digest = '', lease_owner = 'worker-1', fencing_token = %d`,
				run, attempt, candidate, token)
		}
		for name, statement := range map[string]string{
			"a start of development that lowers the attempt":  recordUpdate(toDeveloping+`, attempt = 0`, claimed.IssueID),
			"a start of development that skips an attempt":    recordUpdate(toDeveloping+`, attempt = attempt + 1`, claimed.IssueID),
			"a submission that resets the attempt":            recordUpdate(toEvaluation+`, attempt = 1`, retrying.IssueID),
			"a submission that skips an attempt":              recordUpdate(toEvaluation+`, attempt = 3`, retrying.IssueID),
			"a start of development under another run":        recordUpdate(toDeveloping+`, run_id = 'run-9'`, claimed.IssueID),
			"a start of development that names a candidate":   recordUpdate(toDeveloping+`, candidate_digest = '`+string(candidateDigest)+`'`, claimed.IssueID),
			"a start of development that names a release":     recordUpdate(toDeveloping+`, release_digest = '`+string(releaseDigest)+`'`, claimed.IssueID),
			"a submission that names no candidate":            recordUpdate(`state = 'QAEvaluation', version = version + 1, candidate_digest = ''`, retrying.IssueID),
			"a submission that names a release":               recordUpdate(toEvaluation+`, release_digest = '`+string(releaseDigest)+`'`, retrying.IssueID),
			"a submission under another run":                  recordUpdate(toEvaluation+`, run_id = 'run-9'`, retrying.IssueID),
			"a step under a fencing token never issued":       recordUpdate(toDeveloping+`, fencing_token = fencing_token + 5`, claimed.IssueID),
			"a leased step without a lease owner":             recordUpdate(toDeveloping+`, lease_owner = ''`, claimed.IssueID),
			"a leased step without a fencing token":           recordUpdate(toDeveloping+`, lease_owner = '', fencing_token = 0`, claimed.IssueID),
			"a claim that names the run the record names":     recordUpdate(toClaimed("run-2", 1, "", lease.Token), ready.IssueID),
			"a claim that names a run from before":            recordUpdate(toClaimed("run-1", 1, "", lease.Token), ready.IssueID),
			"a claim that names no run":                       recordUpdate(toClaimed("", 1, "", lease.Token), ready.IssueID),
			"a claim that keeps the attempt count going":      recordUpdate(toClaimed("run-3", 2, "", lease.Token), ready.IssueID),
			"a claim that keeps a candidate":                  recordUpdate(toClaimed("run-3", 1, candidateDigest, lease.Token), ready.IssueID),
			"a claim under the token of a lease taken before": recordUpdate(toClaimed("run-3", 1, "", lease.Token-1), ready.IssueID),
		} {
			refusedRow(t, agent, name, statement)
		}
		// A claim always starts its run at attempt 1, so on a record the
		// store wrote the statement below is the legal start of development.
		// On a Claimed record whose count was raised past the store, it is
		// a reset of the count, and refused.
		raised := claim(t, owner, readyRecord(t, owner, issue(85)), "worker-1", "run-1")
		if _, err := db.Raw(t).ExecContext(ctx, recordUpdate(`attempt = 3`, raised.IssueID)); err != nil {
			t.Fatalf("raise the attempt count past the store: %v", err)
		}
		refusedRow(t, agent, "a start of development that resets the attempt count",
			recordUpdate(`state = 'Developing', version = version + 1, attempt = 1`, raised.IssueID))
		if got := record(t, owner, raised.IssueID); got.State != core.StateClaimed || got.Attempt != 3 {
			t.Fatalf("the refused reset moved the record to %+v", got)
		}

		// Starting a rollout is not the agent zone's step: its role does not
		// see a record in ReleaseCandidate to name a release on it.
		result, err := agent.ExecContext(ctx, recordUpdate(`state = 'Canary', version = version + 1, release_digest = '`+string(releaseDigest)+`'`, releasing.IssueID))
		if err != nil {
			t.Fatalf("a rollout started by the agent role: %v", err)
		}
		if changed, _ := result.RowsAffected(); changed != 0 {
			t.Fatalf("the agent role started the rollout of %d records", changed)
		}
		for _, rec := range []core.StateRecord{claimed, retrying, ready, releasing} {
			if got := record(t, owner, rec.IssueID); got != rec {
				t.Fatalf("a refused change moved %s to %+v", rec.IssueID, got)
			}
		}

		// The same statements, written as the store writes the steps, go
		// through: the premise that each refusal above is for the one thing
		// it changes.
		changedOne(t, agent, "a claim of a run the issue never had", recordUpdate(toClaimed("run-3", 1, "", lease.Token), ready.IssueID))
		changedOne(t, agent, "a submission of the second attempt", recordUpdate(toEvaluation, retrying.IssueID))
		changedOne(t, agent, "a start of development", recordUpdate(toDeveloping, claimed.IssueID))
		if got := record(t, owner, retrying.IssueID); got.Attempt != 2 || got.CandidateDigest != otherCandidate {
			t.Fatalf("the submission wrote %+v", got)
		}
	})
}

// Every step of the lifecycle still goes through for the zone that writes
// it, through the store, over the zone's own database role: the trigger
// refuses no step the store writes.
func TestPostgresEveryZoneTakesEveryLegalStepThroughItsOwnRole(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		control := db.OpenWith(t, logins.DSN[core.ZoneControl], core.ZoneControl)
		agent := db.OpenWith(t, logins.DSN[core.ZoneAgent], core.ZoneAgent)
		taken := map[core.Zone]map[core.Event]bool{core.ZoneControl: {}, core.ZoneAgent: {}}
		zoneOf := map[*sqlstore.Store]core.Zone{control: core.ZoneControl, agent: core.ZoneAgent}
		take := func(s *sqlstore.Store, write core.Transition) core.StateRecord {
			t.Helper()
			taken[zoneOf[s]][write.Event] = true
			return mustTransition(t, s, write)
		}
		walk := func(s *sqlstore.Store, rec core.StateRecord, events ...core.Event) core.StateRecord {
			t.Helper()
			for _, event := range events {
				rec = take(s, next(t, rec, event, nil))
			}
			return rec
		}
		created := func(ref core.IssueRef) core.StateRecord {
			t.Helper()
			return take(control, received(ref))
		}
		claimFor := func(s *sqlstore.Store, rec core.StateRecord, run core.RunID) core.StateRecord {
			t.Helper()
			return take(s, claimWrite(t, s, rec, "worker-1", run))
		}

		// The agent zone runs the work: it claims, develops and submits,
		// and submits again after a failed evaluation; the control zone
		// approves, judges, releases and closes.
		rec := walk(control, created(issue(91)), core.EventInfoMissing, core.EventClarificationCompleted,
			core.EventApprovalRequired, core.EventOwnerApproved)
		rec = walk(agent, claimFor(agent, rec, "run-1"), core.EventDevelopmentStarted, core.EventCandidateSubmitted)
		rec = walk(control, rec, core.EventQAFailed)
		rec = take(agent, next(t, rec, core.EventCandidateSubmitted, func(tr *core.Transition) {
			tr.CandidateDigest, tr.IdempotencyKey = otherCandidate, "candidate/second"
		}))
		rec = walk(control, rec, core.EventQAPassed, core.EventRolloutStarted, core.EventRolloutHealthy,
			core.EventPostDeployVerified, core.EventIssueClosed)
		if rec.State != core.StateClosed || rec.Attempt != 2 || rec.CandidateDigest != otherCandidate || rec.ReleaseDigest != releaseDigest {
			t.Fatalf("the released issue closed as %+v", rec)
		}

		// A denied approval, a retry, and two runs that stop.
		rec = walk(control, created(issue(92)), core.EventInfoSufficient, core.EventApprovalRequired,
			core.EventApprovalDenied, core.EventRetryAuthorized)
		rec = walk(control, claimFor(agent, rec, "run-1"), core.EventClaimStopped)
		rec = take(control, next(t, rec, core.EventRetryAuthorized, func(tr *core.Transition) { tr.IdempotencyKey = "retry/second" }))
		rec = walk(agent, claimFor(agent, rec, "run-2"), core.EventDevelopmentStarted)
		rec = walk(control, rec, core.EventDevelopmentStopped)
		if rec.State != core.StateBlocked || rec.RunID != "run-2" {
			t.Fatalf("the stopped run left %+v", rec)
		}

		// The control zone writes the run's steps itself, and rolls a
		// release back from Canary and from Monitoring.
		rec = walk(control, created(issue(93)), core.EventInfoSufficient, core.EventScopePermitted)
		rec = walk(control, claimFor(control, rec, "run-1"), core.EventDevelopmentStarted, core.EventCandidateSubmitted,
			core.EventQAPassed, core.EventRolloutStarted, core.EventErrorThresholdExceeded, core.EventRecoveryReported)
		if rec.State != core.StateBlocked {
			t.Fatalf("the rolled back release left %+v", rec)
		}
		// It refuses a release, which blocks the issue.
		rec = walk(control, created(issue(95)), core.EventInfoSufficient, core.EventScopePermitted)
		rec = walk(control, claimFor(control, rec, "run-1"), core.EventDevelopmentStarted, core.EventCandidateSubmitted,
			core.EventQAPassed, core.EventReleaseRefused)
		if rec.State != core.StateBlocked {
			t.Fatalf("the refused release left %+v", rec)
		}
		rec = walk(control, created(issue(94)), core.EventInfoSufficient, core.EventScopePermitted)
		rec = walk(control, claimFor(control, rec, "run-1"), core.EventDevelopmentStarted, core.EventCandidateSubmitted,
			core.EventQAPassed, core.EventRolloutStarted, core.EventRolloutHealthy, core.EventRegressionDetected)
		if rec.State != core.StateRolledBack {
			t.Fatalf("the regressed release left %+v", rec)
		}

		// Each zone took every step it may write, so every edge of the
		// lifecycle went through the trigger.
		for zone, events := range taken {
			for _, edge := range statemachine.Edges() {
				if sqlstore.ZoneMayWrite(zone, edge.Event) != events[edge.Event] {
					t.Errorf("the %s zone may write %s: %v, took it: %v", zone, edge.Event, sqlstore.ZoneMayWrite(zone, edge.Event), events[edge.Event])
				}
			}
		}
		for _, edge := range statemachine.Edges() {
			if !slices.ContainsFunc(core.Zones(), func(z core.Zone) bool { return taken[z] != nil && taken[z][edge.Event] }) {
				t.Errorf("no zone took the step %s", edge.Event)
			}
		}
	})
}

// A store migrated by the previous release, whose records stand in every
// part of the lifecycle, keeps them as they are when it is migrated, and
// from then on its records are held to what each step may change while the
// work in flight carries on through the zones' own roles.
func TestPostgresUpgradeHoldsTheRecordsOfAStoreAtMigration8(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		raw := db.Raw(t)
		owner := db.Open(t, core.ZoneControl)

		claimed := claim(t, owner, readyRecord(t, owner, issue(101)), "worker-1", "run-1")
		retrying := developingAtAttempt2(t, owner, issue(102))
		ready := rolledBackAndRetried(t, owner, claim(t, owner, readyRecord(t, owner, issue(103)), "worker-1", "run-1"), "first")
		closed := along(t, owner, developing(t, owner, issue(104)), core.EventCandidateSubmitted, core.EventQAPassed,
			core.EventRolloutStarted, core.EventRolloutHealthy, core.EventPostDeployVerified, core.EventIssueClosed)
		reset := developingAtAttempt2(t, owner, issue(105))

		// The schema as migration 8 left it: the trigger checks the pair of
		// states, the version, the factory and the issue, and nothing else.
		restoreMigration8Trigger(t, raw)
		if _, err := raw.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version > 8`); err != nil {
			t.Fatalf("restore the schema version: %v", err)
		}
		agentRole := rawLogin(t, logins.DSN[core.ZoneAgent])
		changedOne(t, agentRole, "the attempt reset the earlier trigger lets in, the premise of this test",
			recordUpdate(`state = 'QAEvaluation', version = version + 1, attempt = 1, candidate_digest = '`+string(otherCandidate)+`'`, reset.IssueID))
		reset = record(t, owner, reset.IssueID)
		before := []core.StateRecord{claimed, retrying, ready, closed, reset}

		migrator := db.Open(t, core.ZoneControl)
		if err := migrator.Migrate(ctx, state.MigrateOptions{}); err != nil {
			t.Fatalf("migrate a store at migration 8: %v", err)
		}
		if version, err := migrator.SchemaVersion(ctx); err != nil || version != len(sqlstore.Migrations()) {
			t.Fatalf("the store is at version %d after the migration, %v", version, err)
		}
		for _, rec := range before {
			if got := record(t, migrator, rec.IssueID); got != rec {
				t.Fatalf("the migration changed %s from %+v to %+v", rec.IssueID, rec, got)
			}
		}
		var roles string
		if err := raw.QueryRowContext(ctx, `SELECT array_to_string(roles, ',') FROM pg_policies
			WHERE schemaname = current_schema() AND tablename = 'records' AND policyname = 'sisf_agent_update'`).Scan(&roles); err != nil {
			t.Fatalf("read the rewritten policy: %v", err)
		}
		if roles != logins.Roles[core.ZoneAgent] {
			t.Fatalf("the rewritten policy names %q, want the agent role %q", roles, logins.Roles[core.ZoneAgent])
		}

		refusedRow(t, agentRole, "an attempt reset after the migration",
			recordUpdate(`state = 'QAEvaluation', version = version + 1, attempt = 1, candidate_digest = '`+string(otherCandidate)+`'`, retrying.IssueID))

		// The work in flight carries on through the zones' own roles.
		agent := db.OpenWith(t, logins.DSN[core.ZoneAgent], core.ZoneAgent)
		control := db.OpenWith(t, logins.DSN[core.ZoneControl], core.ZoneControl)
		advance(t, agent, claimed, core.EventDevelopmentStarted, nil)
		submitted := advance(t, agent, retrying, core.EventCandidateSubmitted, func(tr *core.Transition) {
			tr.CandidateDigest, tr.IdempotencyKey = otherCandidate, "candidate/after-upgrade"
		})
		advance(t, control, submitted, core.EventQAPassed, nil)
		reclaimed := mustTransition(t, agent, claimWrite(t, agent, ready, "worker-1", "run-2"))
		if reclaimed.RunID != "run-2" || reclaimed.Attempt != 1 {
			t.Fatalf("the claim after the migration wrote %+v", reclaimed)
		}
	})
}

// restoreMigration8Trigger installs the record trigger as migration 8 wrote
// it: the pair of states must be an edge, the version must go up by one,
// and the factory and the issue stay.
func restoreMigration8Trigger(t *testing.T, raw *sql.DB) {
	t.Helper()
	var pairs []string
	for _, edge := range statemachine.Edges() {
		if edge.From != "" {
			pairs = append(pairs, "('"+string(edge.From)+"', '"+string(edge.To)+"')")
		}
	}
	if _, err := raw.ExecContext(ctx, `CREATE OR REPLACE FUNCTION sisf_record_step() RETURNS trigger
		LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $migration8$
		BEGIN
			IF pg_has_role(current_user, (SELECT relowner FROM pg_class WHERE oid = TG_RELID), 'USAGE')
				OR EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND rolbypassrls) THEN
				RETURN NEW;
			END IF;
			IF NEW.factory_id = OLD.factory_id AND NEW.issue = OLD.issue AND NEW.version = OLD.version + 1
				AND (OLD.state, NEW.state) IN (`+strings.Join(pairs, ", ")+`) THEN
				RETURN NEW;
			END IF;
			RAISE EXCEPTION 'a lifecycle record moves only along one step of the lifecycle, to its next version'
				USING ERRCODE = 'insufficient_privilege';
		END
		$migration8$`); err != nil {
		t.Fatalf("restore the trigger of migration 8: %v", err)
	}
}
