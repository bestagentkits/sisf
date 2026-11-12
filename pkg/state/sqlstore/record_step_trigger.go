package sqlstore

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

// recordStepFunction names the trigger of recordStepTrigger and its
// function.
const recordStepFunction = "sisf_record_step"

// leasePrefixPattern is what the prefix of the issue lease's resource must
// look like before it is written into the trigger as a literal.
var leasePrefixPattern = regexp.MustCompile(`^[a-z]+:$`)

// recordStepTrigger returns the statements that install the trigger which
// holds every update of a record, by a session that row-level security
// holds, to one step of the lifecycle as the store writes it (Transition):
//
//   - the pair of the old and the new state is an edge of
//     statemachine.Edges, the version goes up by one, and the factory and
//     the issue stay as they are;
//   - the run, the attempt, the candidate and the release change only as
//     the step of that edge changes them (stepColumns);
//   - a step that a run's lease covers carries a lease owner and a fencing
//     token, and any token a step carries is the latest one issued for the
//     issue's lease (statemachine.CheckLease and Tx.CheckFencing);
//   - a claim names a run the journal shows the issue has never had
//     (checkNewRun).
//
// Anything else is refused with insufficient_privilege. Which zone may take
// which step is the row policies' to say: with the policy of a zone on the
// states its steps start and end in, the edge rule admits exactly the
// zone's own steps, so only the zones that submit a candidate or start a
// rollout can set the candidate or the release.
//
// The trigger leaves alone a session row-level security does not hold
// either: the owner of the table, a superuser and a role that bypasses row
// security. They install, migrate and repair the schema. The function runs
// with pg_catalog alone on its search path, so a session cannot slip its
// own functions or operators into it; it names the journal and the leases
// in the schema of the records it guards.
func recordStepTrigger() (string, error) {
	var steps []string
	seen := map[[2]core.State]core.Event{}
	for _, edge := range statemachine.Edges() {
		if edge.From == "" {
			continue
		}
		// The trigger sees the states and not the event, so each pair of
		// states must be one step only.
		pair := [2]core.State{edge.From, edge.To}
		if other, taken := seen[pair]; taken {
			return "", core.NewError(core.CodeInternal, fmt.Sprintf(
				"the steps %q and %q both move a record from %s to %s; the trigger cannot tell them apart",
				string(other), string(edge.Event), string(edge.From), string(edge.To)))
		}
		seen[pair] = edge.Event
		step, err := stepCondition(edge)
		if err != nil {
			return "", err
		}
		steps = append(steps, step)
	}
	slices.Sort(steps)
	leasePrefix, err := issueLeasePrefix()
	if err != nil {
		return "", err
	}
	refuse := func(why string) string {
		return `RAISE EXCEPTION '` + why + `' USING ERRCODE = 'insufficient_privilege';`
	}
	return `CREATE OR REPLACE FUNCTION ` + quote(recordStepFunction) + `() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $sisf_record_step$
DECLARE
	used boolean;
	latest bigint;
BEGIN
	IF pg_has_role(current_user, (SELECT relowner FROM pg_class WHERE oid = TG_RELID), 'USAGE')
		OR EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND rolbypassrls) THEN
		RETURN NEW;
	END IF;
	IF NOT (NEW.factory_id = OLD.factory_id AND NEW.issue = OLD.issue AND NEW.version = OLD.version + 1
		AND NEW.idempotency_key <> '' AND NEW.fencing_token >= 0 AND (NEW.fencing_token = 0 OR NEW.lease_owner <> '')
		AND (
		` + strings.Join(steps, "\n\t\tOR ") + `)) THEN
		` + refuse("a lifecycle record moves only along one step of the lifecycle, to its next version, and changes only what that step sets") + `
	END IF;
	IF NEW.run_id <> OLD.run_id THEN
		EXECUTE format('SELECT EXISTS (SELECT 1 FROM %I.` + quote(tableTransitions) + ` WHERE factory_id = $1 AND issue = $2 AND run_id = $3)', TG_TABLE_SCHEMA)
			INTO used USING NEW.factory_id, NEW.issue, NEW.run_id;
		IF used THEN
			` + refuse("a claim starts a run the issue has not had") + `
		END IF;
	END IF;
	IF NEW.fencing_token <> 0 THEN
		EXECUTE format('SELECT token FROM %I.` + quote(tableLeases) + ` WHERE factory_id = $1 AND resource = $2', TG_TABLE_SCHEMA)
			INTO latest USING NEW.factory_id, ` + leasePrefix + ` || NEW.issue;
		IF latest IS NULL OR latest <> NEW.fencing_token THEN
			` + refuse("a lifecycle step carries the latest fencing token of the issue lease") + `
		END IF;
	END IF;
	RETURN NEW;
END
$sisf_record_step$;
DROP TRIGGER IF EXISTS ` + quote(recordStepFunction) + ` ON ` + quote(tableRecords) + `;
CREATE TRIGGER ` + quote(recordStepFunction) + ` BEFORE UPDATE ON ` + quote(tableRecords) + `
	FOR EACH ROW EXECUTE FUNCTION ` + quote(recordStepFunction) + `();
`, nil
}

// stepCondition is the condition under which an update is the step of
// edge: the record leaves edge.From for edge.To and changes what that step
// changes, and nothing else the step is bound to.
func stepCondition(edge statemachine.Edge) (string, error) {
	from, err := literal(string(edge.From), literalPattern)
	if err != nil {
		return "", err
	}
	to, err := literal(string(edge.To), literalPattern)
	if err != nil {
		return "", err
	}
	conditions := append([]string{"(OLD.state, NEW.state) = (" + from + ", " + to + ")"}, stepColumns(edge.Event)...)
	if statemachine.RequiresLease(edge.From) || statemachine.RequiresLease(edge.To) {
		conditions = append(conditions, "NEW.lease_owner <> ''", "NEW.fencing_token > 0")
	}
	return "(" + strings.Join(conditions, " AND ") + ")", nil
}

// stepColumns says how the step on event leaves the run, the attempt, the
// candidate and the release of a record, as the store writes it (written)
// and as statemachine.CheckRecordBinding requires: the claim starts a new
// run at attempt 1 and forgets the candidate and the release of the run
// before it; a failed evaluation sends the run into its next attempt;
// submitting a candidate names it and starting the rollout names the
// release; every other step keeps all four.
func stepColumns(event core.Event) []string {
	run, attempt := "NEW.run_id = OLD.run_id", "NEW.attempt = OLD.attempt"
	candidate, release := "NEW.candidate_digest = OLD.candidate_digest", "NEW.release_digest = OLD.release_digest"
	switch event {
	case core.EventClaimed:
		run, attempt = "NEW.run_id <> '' AND NEW.run_id <> OLD.run_id", "NEW.attempt = 1"
		candidate, release = "NEW.candidate_digest = ''", "NEW.release_digest = ''"
	case core.EventQAFailed:
		attempt = "NEW.attempt = OLD.attempt + 1"
	case core.EventCandidateSubmitted:
		candidate = "NEW.candidate_digest <> ''"
	case core.EventRolloutStarted:
		release = "NEW.release_digest <> ''"
	}
	return []string{run, attempt, candidate, release}
}

// issueLeasePrefix is the prefix state.IssueLeaseResource puts before the
// issue to name the lease on it, as a literal for the trigger.
func issueLeasePrefix() (string, error) {
	sample := core.IssueRef{Owner: "o", Repo: "r", Number: 1}
	prefix, found := strings.CutSuffix(state.IssueLeaseResource(sample), sample.String())
	if !found {
		return "", core.NewError(core.CodeInternal, "the issue lease is not named after its issue")
	}
	return literal(prefix, leasePrefixPattern)
}
