package sqlstore_test

import (
	"database/sql"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// A failed evaluation is judged and written by the supervisor's QA stage in
// the control zone. A run never sends its own candidate back for another
// attempt.
func TestAgentZoneDoesNotFailAnEvaluation(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control := db.Open(t, core.ZoneControl)
		agent := db.Open(t, core.ZoneAgent)
		developed := along(t, agent, claim(t, agent, readyRecord(t, control, issue(3)), "worker-1", "run-1"),
			core.EventDevelopmentStarted)
		evaluating := advance(t, control, developed, core.EventCandidateSubmitted, nil)
		refused(t, agent, "qa failure written by the agent zone", next(t, evaluating, core.EventQAFailed, nil), core.CodePermissionDenied)
		if retried := advance(t, control, evaluating, core.EventQAFailed, nil); retried.Attempt != 2 {
			t.Fatalf("the control zone's qa failure wrote %+v", retried)
		}
	})
}

// The row policy of a zone on the records sees the state a row starts in
// and the state it ends in, each on its own. Together with the rule that a
// row moves only along one edge of the lifecycle, that admits exactly the
// zone's own steps and no pair of states in between.
func TestTheStatesOfAZonesStepsAdmitNoOtherStep(t *testing.T) {
	t.Parallel()
	for _, zone := range core.Zones() {
		from, to, own := map[core.State]bool{}, map[core.State]bool{}, map[statemachine.Edge]bool{}
		for _, edge := range statemachine.Edges() {
			if edge.From == "" || !sqlstore.ZoneMayWrite(zone, edge.Event) {
				continue
			}
			from[edge.From], to[edge.To], own[edge] = true, true, true
		}
		for _, edge := range statemachine.Edges() {
			if edge.From != "" && from[edge.From] && to[edge.To] && !own[edge] {
				t.Errorf("the %s zone may move a record from %s to %s, the step %s it may not write",
					zone, edge.From, edge.To, edge.Event)
			}
		}
	}
}

// A stolen agent-zone credential used with plain SQL moves a record only
// along one step of its own, to the next version, and never to another
// issue: it cannot skip a step or rewrite a record in place.
func TestPostgresMovesARecordOnlyAlongOneStepOfTheLifecycle(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		assertRecordStepRules(t, db, rawLogin(t, logins.DSN[core.ZoneAgent]))
	})
}

// The migration holds a store whose roles an earlier release granted to
// the same rules, keeping the roles its policies name.
func TestPostgresMigrationHoldsAGrantedStoreToLifecycleSteps(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		raw := db.Raw(t)
		agentRole := logins.Roles[core.ZoneAgent]
		// The schema and the policy as the earlier release left them: no
		// trigger, and a policy on the records that lets the agent role
		// leave QAEvaluation.
		forgetStoreRules(t, raw)
		for _, statement := range []string{
			`DROP TRIGGER IF EXISTS sisf_record_step ON records`,
			`DROP POLICY "sisf_agent_update" ON records`,
			`CREATE POLICY "sisf_agent_update" ON records FOR UPDATE TO "` + agentRole + `"
				USING (state IN ('Claimed', 'Developing', 'QAEvaluation', 'Ready'))
				WITH CHECK (state IN ('Claimed', 'Developing', 'QAEvaluation'))`,
		} {
			if _, err := raw.ExecContext(ctx, statement); err != nil {
				t.Fatalf("restore the earlier schema: %v", err)
			}
		}
		agent := rawLogin(t, logins.DSN[core.ZoneAgent])
		owner := db.Open(t, core.ZoneControl)
		skipped := readyRecord(t, owner, issue(404))
		if _, err := agent.ExecContext(ctx, `UPDATE records SET state = 'Developing' WHERE issue = $1`, skipped.IssueID.String()); err != nil {
			t.Fatalf("the earlier schema should let the skipped step in, the premise of this test: %v", err)
		}

		script, err := sqlstore.MigrationScript(7, sqlstore.DialectPostgres)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.ExecContext(ctx, script); err != nil {
			t.Fatalf("apply the migration to a granted store: %v", err)
		}
		var roles string
		if err := raw.QueryRowContext(ctx, `SELECT array_to_string(roles, ',') FROM pg_policies
			WHERE schemaname = current_schema() AND tablename = 'records' AND policyname = 'sisf_agent_update'`).Scan(&roles); err != nil {
			t.Fatalf("read the rewritten policy: %v", err)
		}
		if roles != agentRole {
			t.Fatalf("the rewritten policy names %q, want the agent role %q", roles, agentRole)
		}
		assertRecordStepRules(t, db, agent)
		assertAgentJournalRules(t, db, agent)
	})
}

// assertRecordStepRules checks, over the agent role's own connection, that a
// record moves only along one step of the agent zone's own.
func assertRecordStepRules(t *testing.T, db *sqlstoretest.Database, agent *sql.DB) {
	t.Helper()
	control := db.Open(t, core.ZoneControl)
	ready := readyRecord(t, control, issue(31))
	claimed := claim(t, control, readyRecord(t, control, issue(32)), "worker-1", "run-1")
	evaluating := along(t, control, claim(t, control, readyRecord(t, control, issue(33)), "worker-1", "run-1"),
		core.EventDevelopmentStarted, core.EventCandidateSubmitted)

	update := func(set string, ref core.IssueRef) string {
		return `UPDATE records SET ` + set + ` WHERE factory_id = '` + string(sqlstoretest.Factory) + `' AND issue = '` + ref.String() + `'`
	}
	for name, statement := range map[string]string{
		"Ready straight to Developing":            update(`state = 'Developing', version = version + 1`, ready.IssueID),
		"Claimed straight to QAEvaluation":        update(`state = 'QAEvaluation', version = version + 1`, claimed.IssueID),
		"a step that keeps the version":           update(`state = 'Developing'`, claimed.IssueID),
		"a step that skips a version":             update(`state = 'Developing', version = version + 2`, claimed.IssueID),
		"a step that rewinds the version":         update(`state = 'Developing', version = 1`, claimed.IssueID),
		"a record rewritten in place":             update(`run_id = 'run-9', version = version + 1`, claimed.IssueID),
		"a step that moves the record elsewhere":  update(`state = 'Developing', version = version + 1, issue = 'acme/app#77'`, claimed.IssueID),
		"a step that moves it to another factory": update(`state = 'Developing', version = version + 1, factory_id = 'other'`, claimed.IssueID),
	} {
		refusedRow(t, agent, name, statement)
	}
	// A record in QAEvaluation is past the agent zone's steps: the agent
	// role does not see it to change it.
	result, err := agent.ExecContext(ctx, update(`state = 'Developing', version = version + 1`, evaluating.IssueID))
	if err != nil {
		t.Fatalf("a failed evaluation written by the agent role: %v", err)
	}
	if changed, _ := result.RowsAffected(); changed != 0 {
		t.Fatalf("the agent role failed an evaluation on %d records", changed)
	}
	for _, rec := range []core.StateRecord{ready, claimed, evaluating} {
		if got := record(t, control, rec.IssueID); got != rec {
			t.Fatalf("a refused change moved %s to %+v", rec.IssueID, got)
		}
	}
	// The one step the agent zone may take from Claimed goes through, with
	// its journal row.
	rawStep(t, agent, "the agent role's own step", claimed.IssueID,
		`state = 'Developing', version = version + 1, idempotency_key = 'develop/run-1'`, core.StateClaimed, core.EventDevelopmentStarted)
}
