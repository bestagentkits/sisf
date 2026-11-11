package core

import "slices"

// State is one of the fifteen states of the issue lifecycle. The set is
// closed: no component may invent a state, and the text form is what the
// operational store persists.
type State string

const (
	StateIntake           State = "Intake"
	StateClarifying       State = "Clarifying"
	StateScopeDraft       State = "ScopeDraft"
	StateApprovalPending  State = "ApprovalPending"
	StateReady            State = "Ready"
	StateClaimed          State = "Claimed"
	StateDeveloping       State = "Developing"
	StateQAEvaluation     State = "QAEvaluation"
	StateReleaseCandidate State = "ReleaseCandidate"
	StateCanary           State = "Canary"
	StateMonitoring       State = "Monitoring"
	StateVerified         State = "Verified"
	StateClosed           State = "Closed"
	StateRolledBack       State = "RolledBack"
	StateBlocked          State = "Blocked"
)

var allStates = []State{
	StateIntake, StateClarifying, StateScopeDraft, StateApprovalPending, StateReady,
	StateClaimed, StateDeveloping, StateQAEvaluation, StateReleaseCandidate, StateCanary,
	StateMonitoring, StateVerified, StateClosed, StateRolledBack, StateBlocked,
}

// States returns the fifteen lifecycle states in lifecycle order. The caller
// owns the returned slice.
func States() []State { return slices.Clone(allStates) }

// String returns the persisted text form.
func (s State) String() string { return string(s) }

// Valid reports whether s is one of the fifteen states.
func (s State) Valid() bool { return slices.Contains(allStates, s) }

// ParseState converts persisted text back to a State.
func ParseState(text string) (State, error) {
	if s := State(text); s.Valid() {
		return s, nil
	}
	return "", invalid("unknown issue state %q", text)
}

// Event names the cause of one lifecycle edge. Each edge of the lifecycle has
// exactly one event, so a recorded event identifies the edge that was taken.
// Which state an event leads to is decided by the transition table, not here.
type Event string

const (
	// EventIssueReceived creates the record in Intake.
	EventIssueReceived Event = "issue_received"
	// EventInfoMissing moves Intake to Clarifying.
	EventInfoMissing Event = "info_missing"
	// EventClarificationCompleted moves Clarifying to ScopeDraft.
	EventClarificationCompleted Event = "clarification_completed"
	// EventInfoSufficient moves Intake to ScopeDraft.
	EventInfoSufficient Event = "info_sufficient"
	// EventApprovalRequired moves ScopeDraft to ApprovalPending: a new feature
	// or a change outside the delegated scope.
	EventApprovalRequired Event = "approval_required"
	// EventScopePermitted moves ScopeDraft to Ready: policy permits the scope.
	EventScopePermitted Event = "scope_permitted"
	// EventOwnerApproved moves ApprovalPending to Ready.
	EventOwnerApproved Event = "owner_approved"
	// EventApprovalDenied moves ApprovalPending to Blocked: rejected or expired.
	EventApprovalDenied Event = "approval_denied"
	// EventClaimed moves Ready to Claimed under a lease and a budget reservation.
	EventClaimed Event = "claimed"
	// EventDevelopmentStarted moves Claimed to Developing.
	EventDevelopmentStarted Event = "development_started"
	// EventClaimStopped moves Claimed to Blocked: the run stopped before any
	// agent attempt started working, and the issue waits for the Owner.
	EventClaimStopped Event = "claim_stopped"
	// EventDevelopmentStopped moves Developing to Blocked: the run stopped
	// without a candidate to evaluate, and the issue waits for the Owner.
	EventDevelopmentStopped Event = "development_stopped"
	// EventCandidateSubmitted moves Developing to QAEvaluation: patch, tests
	// and artifacts exist.
	EventCandidateSubmitted Event = "candidate_submitted"
	// EventQAFailed moves QAEvaluation back to Developing while a retry is allowed.
	EventQAFailed Event = "qa_failed"
	// EventQAPassed moves QAEvaluation to ReleaseCandidate: the score and
	// every hard gate pass.
	EventQAPassed Event = "qa_passed"
	// EventRolloutStarted moves ReleaseCandidate to Canary.
	EventRolloutStarted Event = "rollout_started"
	// EventRolloutHealthy moves Canary to Monitoring.
	EventRolloutHealthy Event = "rollout_healthy"
	// EventErrorThresholdExceeded moves Canary to RolledBack.
	EventErrorThresholdExceeded Event = "error_threshold_exceeded"
	// EventPostDeployVerified moves Monitoring to Verified: the observed
	// revision meets the DoD through the observation window.
	EventPostDeployVerified Event = "post_deploy_verified"
	// EventRegressionDetected moves Monitoring to RolledBack.
	EventRegressionDetected Event = "regression_detected"
	// EventIssueClosed moves Verified to Closed.
	EventIssueClosed Event = "issue_closed"
	// EventRecoveryReported moves RolledBack to Blocked with evidence.
	EventRecoveryReported Event = "recovery_reported"
	// EventRetryAuthorized moves Blocked to Ready: a safe, authorized retry.
	EventRetryAuthorized Event = "retry_authorized"
)

var allEvents = []Event{
	EventIssueReceived, EventInfoMissing, EventClarificationCompleted, EventInfoSufficient,
	EventApprovalRequired, EventScopePermitted, EventOwnerApproved, EventApprovalDenied,
	EventClaimed, EventDevelopmentStarted, EventClaimStopped, EventDevelopmentStopped,
	EventCandidateSubmitted, EventQAFailed, EventQAPassed, EventRolloutStarted, EventRolloutHealthy, EventErrorThresholdExceeded,
	EventPostDeployVerified, EventRegressionDetected, EventIssueClosed, EventRecoveryReported,
	EventRetryAuthorized,
}

// Events returns every lifecycle event in lifecycle order. The caller owns
// the returned slice.
func Events() []Event { return slices.Clone(allEvents) }

// String returns the persisted text form.
func (e Event) String() string { return string(e) }

// Valid reports whether e is a lifecycle event.
func (e Event) Valid() bool { return slices.Contains(allEvents, e) }

// ParseEvent converts persisted text back to an Event.
func ParseEvent(text string) (Event, error) {
	if e := Event(text); e.Valid() {
		return e, nil
	}
	return "", invalid("unknown lifecycle event %q", text)
}
