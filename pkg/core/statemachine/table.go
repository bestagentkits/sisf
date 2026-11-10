// Package statemachine enforces the issue lifecycle: which state an event
// leads to, and which proof a step needs before it may be taken.
//
// The transition table is the single list of legal steps. It holds the edge
// that creates a record in Intake and the twenty edges between the fifteen
// states; a step that is not in the table is refused with INVALID_TRANSITION,
// so no component can add a state or a shortcut by writing one. Seven edges
// are guarded: a guard supplies the facts and the machine judges them, and a
// step whose proof is absent or does not hold is refused with GUARD_FAILED.
// Every way into Ready and every step back to an earlier state is among them.
//
// The package decides whether a step is legal. Writing it, with the version
// check and the fencing check, is the job of the operational store, which
// asks CheckEdge again as it writes so that no caller can skip the table.
package statemachine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// GuardKind names the proof an edge needs. Most edges need none.
type GuardKind string

const (
	// GuardNone marks an edge that only the table constrains.
	GuardNone GuardKind = ""
	// GuardDelegation protects ScopeDraft to Ready: proof that policy lets the
	// scope proceed without the Owner. Without it the only way on is the
	// approval request.
	GuardDelegation GuardKind = "delegation"
	// GuardApproval protects ApprovalPending to Ready: a signed, unexpired,
	// unused approval by a person, bound to the scope and the policy in force.
	GuardApproval GuardKind = "approval"
	// GuardClaim protects Ready to Claimed: a live lease and a held budget
	// reservation for the run being started.
	GuardClaim GuardKind = "claim"
	// GuardAttempts protects QAEvaluation back to Developing: the run has
	// attempts left and the retry counts as the next one.
	GuardAttempts GuardKind = "attempts"
	// GuardQAPass protects QAEvaluation to ReleaseCandidate: a valid receipt
	// for this issue, run and candidate, every hard gate passing and the score
	// reaching the pass score.
	GuardQAPass GuardKind = "qa_pass"
	// GuardPostDeploy protects Monitoring to Verified: the released revision
	// observed running after its observation window, with the DoD met.
	GuardPostDeploy GuardKind = "post_deploy"
	// GuardRetry protects Blocked to Ready: a retry someone was authorized to ask for.
	GuardRetry GuardKind = "retry"
)

// Edge is one legal step of the lifecycle. From is empty on the edge that
// creates the record.
type Edge struct {
	From  core.State
	Event core.Event
	To    core.State
	Guard GuardKind
}

// noRecord is the source of the creation edge: the issue has no record yet.
const noRecord core.State = ""

// lifecycle is the transition table, in lifecycle order. Closed is entered
// only from Verified and has no edge out.
var lifecycle = []Edge{
	{noRecord, core.EventIssueReceived, core.StateIntake, GuardNone},
	{core.StateIntake, core.EventInfoMissing, core.StateClarifying, GuardNone},
	{core.StateClarifying, core.EventClarificationCompleted, core.StateScopeDraft, GuardNone},
	{core.StateIntake, core.EventInfoSufficient, core.StateScopeDraft, GuardNone},
	{core.StateScopeDraft, core.EventApprovalRequired, core.StateApprovalPending, GuardNone},
	{core.StateScopeDraft, core.EventScopePermitted, core.StateReady, GuardDelegation},
	{core.StateApprovalPending, core.EventOwnerApproved, core.StateReady, GuardApproval},
	{core.StateApprovalPending, core.EventApprovalDenied, core.StateBlocked, GuardNone},
	{core.StateReady, core.EventClaimed, core.StateClaimed, GuardClaim},
	{core.StateClaimed, core.EventDevelopmentStarted, core.StateDeveloping, GuardNone},
	{core.StateDeveloping, core.EventCandidateSubmitted, core.StateQAEvaluation, GuardNone},
	{core.StateQAEvaluation, core.EventQAFailed, core.StateDeveloping, GuardAttempts},
	{core.StateQAEvaluation, core.EventQAPassed, core.StateReleaseCandidate, GuardQAPass},
	{core.StateReleaseCandidate, core.EventRolloutStarted, core.StateCanary, GuardNone},
	{core.StateCanary, core.EventRolloutHealthy, core.StateMonitoring, GuardNone},
	{core.StateCanary, core.EventErrorThresholdExceeded, core.StateRolledBack, GuardNone},
	{core.StateMonitoring, core.EventPostDeployVerified, core.StateVerified, GuardPostDeploy},
	{core.StateMonitoring, core.EventRegressionDetected, core.StateRolledBack, GuardNone},
	{core.StateVerified, core.EventIssueClosed, core.StateClosed, GuardNone},
	{core.StateRolledBack, core.EventRecoveryReported, core.StateBlocked, GuardNone},
	{core.StateBlocked, core.EventRetryAuthorized, core.StateReady, GuardRetry},
}

// Edges returns the transition table in lifecycle order. The caller owns the
// returned slice; changing it does not change what the machine accepts.
func Edges() []Edge { return slices.Clone(lifecycle) }

// Next returns the state that event leads to from the given state. An empty
// from asks about an issue that has no record yet. Any pair outside the
// table, including unknown states and events, yields INVALID_TRANSITION.
//
// Next reads the table only. A caller that is about to write a step asks
// Machine.Check, which also runs the guard of the edge.
func Next(from core.State, event core.Event) (core.State, error) {
	edge, ok := lookup(from, event)
	if !ok {
		return "", noEdge(from, event, "")
	}
	return edge.To, nil
}

// CheckEdge reports whether t is a step of the lifecycle: the table has an
// edge from t.From on t.Event and that edge leads to t.To. Any other step,
// such as Intake straight to Closed, yields INVALID_TRANSITION.
//
// The check is a pure lookup, equal to asking Next for t.From and t.Event and
// comparing the answer with t.To. It reads no clock, consults no guard and
// judges nothing else in the request. An operational store calls it in every
// write of a transition, so the table holds even for a caller that never
// asked the machine; Machine.Check applies the same check before any guard.
func CheckEdge(t core.Transition) error {
	_, err := edgeOf(t)
	return err
}

// edgeOf returns the table edge that t follows, or INVALID_TRANSITION.
func edgeOf(t core.Transition) (Edge, error) {
	edge, ok := lookup(t.From, t.Event)
	if !ok || edge.To != t.To {
		return Edge{}, noEdge(t.From, t.Event, t.To)
	}
	return edge, nil
}

// lookup finds the edge leaving from on event. Each event drives one edge,
// so a linear scan of the table is unambiguous.
func lookup(from core.State, event core.Event) (Edge, bool) {
	for _, edge := range lifecycle {
		if edge.From == from && edge.Event == event {
			return edge, true
		}
	}
	return Edge{}, false
}

// eventsFrom lists the events the given state accepts, in lifecycle order.
func eventsFrom(from core.State) []core.Event {
	var events []core.Event
	for _, edge := range lifecycle {
		if edge.From == from {
			events = append(events, edge.Event)
		}
	}
	return events
}

// noEdge builds the refusal for a step the table does not contain. Its
// details list the events the source state does accept, so a caller can pick
// a valid step without guessing. to is empty when no target was requested.
func noEdge(from core.State, event core.Event, to core.State) *core.Error {
	source := "an issue in " + string(from)
	if from == noRecord {
		source = "an issue without a record"
	}
	message := fmt.Sprintf("the issue lifecycle has no step for %s on event %q", source, string(event))
	details := map[string]string{"from": string(from), "event": string(event)}
	if to != "" {
		message += " to " + string(to)
		details["to"] = string(to)
	}
	if allowed := eventsFrom(from); len(allowed) > 0 {
		names := make([]string, len(allowed))
		for i, e := range allowed {
			names[i] = string(e)
		}
		details["allowed_events"] = strings.Join(names, ",")
	}
	refusal := core.NewError(core.CodeInvalidTransition, message)
	refusal.Details = details
	return refusal
}
