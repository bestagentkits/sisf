// Package statemachine enforces the issue lifecycle: which state an event
// leads to, and which proof a step needs before it may be taken.
//
// The transition table is the single list of legal steps. It holds the edge
// that creates a record in Intake and the twenty-three edges between the fifteen
// states; a step that is not in the table is refused with INVALID_TRANSITION,
// so no component can add a state or a shortcut by writing one. Ten edges are
// guarded: a guard supplies the facts and the machine judges them, and a step
// whose proof is absent or does not hold is refused, with GUARD_FAILED or,
// when a release cannot be observed or rolled back, with RELEASE_HOLD. Every
// way into Ready, every step back to an earlier state and every step that
// moves a candidate towards users is among them. The table says, edge by
// edge, why each of the others needs no proof.
//
// From the claim until the release leaves Monitoring a run holds the issue,
// and every step must be written under its lease: RequiresLease names those
// states and CheckLease refuses a step that carries no lease.
//
// The package decides whether a step is legal. Writing it, with the version
// check, the fencing check and the check against the stored run, is the job
// of the operational store, which asks CheckEdge, CheckLease and
// CheckRecordBinding again as it writes so that no caller can skip them.
package statemachine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// GuardKind names the proof an edge needs.
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
	// GuardCandidate protects Developing to QAEvaluation: the request itself
	// must name the run and the built candidate it submits. The machine reads
	// both from the request, so no guard has to be wired for it.
	GuardCandidate GuardKind = "candidate"
	// GuardAttempts protects QAEvaluation back to Developing: the run has
	// attempts left and the retry counts as the next one.
	GuardAttempts GuardKind = "attempts"
	// GuardQAPass protects QAEvaluation to ReleaseCandidate: a valid receipt
	// for this issue, run and candidate, every hard gate passing and the score
	// reaching the pass score.
	GuardQAPass GuardKind = "qa_pass"
	// GuardRollout protects ReleaseCandidate to Canary: the rollback
	// preflight. A verified signed artifact of this factory for the candidate
	// QA passed, a verified revision to roll back to and readable metrics;
	// without a way back or a way to observe, the release holds.
	GuardRollout GuardKind = "rollout_preflight"
	// GuardCanaryHealth protects Canary to Monitoring: the release observed
	// running and healthy, read from the running system with its metrics.
	GuardCanaryHealth GuardKind = "canary_health"
	// GuardPostDeploy protects Monitoring to Verified: the released revision
	// observed running after its observation window, with the DoD met.
	GuardPostDeploy GuardKind = "post_deploy"
	// GuardRetry protects Blocked to Ready: a retry a person or a service was
	// authorized to ask for, which cannot undo a denied approval.
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
//
// An edge is left unguarded only when taking it can start no work, spend
// nothing and release nothing, or when it moves to a safer state and must
// never wait for a proof. The comment on each such edge says which.
var lifecycle = []Edge{
	// Intake grants nothing: a received issue is a request, not a permission.
	{noRecord, core.EventIssueReceived, core.StateIntake, GuardNone},
	// The three steps of scoping only record how far the conversation about
	// the issue got. Nothing runs before Ready, and every way into Ready is
	// guarded, so a scope drafted too early still proves nothing.
	{core.StateIntake, core.EventInfoMissing, core.StateClarifying, GuardNone},
	{core.StateClarifying, core.EventClarificationCompleted, core.StateScopeDraft, GuardNone},
	{core.StateIntake, core.EventInfoSufficient, core.StateScopeDraft, GuardNone},
	// Asking the Owner is the stricter of the two ways out of ScopeDraft, so
	// taking it without need costs a decision and widens nothing.
	{core.StateScopeDraft, core.EventApprovalRequired, core.StateApprovalPending, GuardNone},
	{core.StateScopeDraft, core.EventScopePermitted, core.StateReady, GuardDelegation},
	{core.StateApprovalPending, core.EventOwnerApproved, core.StateReady, GuardApproval},
	// Blocking stops the issue. A rejected or lapsed request must always be
	// able to block it, and the way out of Blocked is guarded.
	{core.StateApprovalPending, core.EventApprovalDenied, core.StateBlocked, GuardNone},
	{core.StateReady, core.EventClaimed, core.StateClaimed, GuardClaim},
	// Starting work adds no authority to the claim: the step is written under
	// the lease the claim took and by the run it started, which the lease
	// check and the store's check against the stored run enforce.
	{core.StateClaimed, core.EventDevelopmentStarted, core.StateDeveloping, GuardNone},
	// A run that stopped without a candidate, because it used every attempt
	// or because its work can no longer be built, must always be able to
	// block its issue: Blocked starts no work, spends nothing and releases
	// nothing, and the way out of it is guarded. Only the control zone
	// writes these steps, under the issue lease, for the run the record
	// names.
	{core.StateClaimed, core.EventClaimStopped, core.StateBlocked, GuardNone},
	{core.StateDeveloping, core.EventDevelopmentStopped, core.StateBlocked, GuardNone},
	{core.StateDeveloping, core.EventCandidateSubmitted, core.StateQAEvaluation, GuardCandidate},
	{core.StateQAEvaluation, core.EventQAFailed, core.StateDeveloping, GuardAttempts},
	{core.StateQAEvaluation, core.EventQAPassed, core.StateReleaseCandidate, GuardQAPass},
	// A release the supervisor refuses before anything is deployed, because
	// the candidate cannot be verified or is not one the factory may publish
	// by itself, must always be able to block its issue rather than leave it
	// waiting for a release that never comes. Blocked starts no work, spends
	// nothing and releases nothing, and the way out of it is guarded. Only
	// the control zone, where the supervisor writes, takes this step, under
	// the issue lease, for the run the record names.
	{core.StateReleaseCandidate, core.EventReleaseRefused, core.StateBlocked, GuardNone},
	{core.StateReleaseCandidate, core.EventRolloutStarted, core.StateCanary, GuardRollout},
	{core.StateCanary, core.EventRolloutHealthy, core.StateMonitoring, GuardCanaryHealth},
	// A rollback is the safe direction and must never wait for a proof that
	// may be unobtainable while the release is failing. Only the holder of
	// the lease can report it, and it ends in Blocked.
	{core.StateCanary, core.EventErrorThresholdExceeded, core.StateRolledBack, GuardNone},
	{core.StateMonitoring, core.EventPostDeployVerified, core.StateVerified, GuardPostDeploy},
	// As above: a regression rolls the release back without a proof.
	{core.StateMonitoring, core.EventRegressionDetected, core.StateRolledBack, GuardNone},
	// Verified is entered only on the post-deploy proof, which is the proof
	// that the issue may close; closing records it and adds nothing.
	{core.StateVerified, core.EventIssueClosed, core.StateClosed, GuardNone},
	// The recovery report is evidence for people, not a permission: Blocked
	// grants nothing and the way out of it is guarded.
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
