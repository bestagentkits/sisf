package core

import (
	"slices"
	"testing"
)

func TestIssueLifecycleHasExactlyTheFifteenStates(t *testing.T) {
	want := []State{
		"Intake", "Clarifying", "ScopeDraft", "ApprovalPending", "Ready",
		"Claimed", "Developing", "QAEvaluation", "ReleaseCandidate", "Canary",
		"Monitoring", "Verified", "Closed", "RolledBack", "Blocked",
	}
	got := States()
	if !slices.Equal(got, want) {
		t.Fatalf("States() = %v, want %v", got, want)
	}
	for _, s := range got {
		if !s.Valid() {
			t.Errorf("state %q is listed but not valid", s)
		}
		if s.String() != string(s) {
			t.Errorf("state %q renders as %q", string(s), s.String())
		}
	}
}

func TestEveryLifecycleEdgeHasOneNamedEvent(t *testing.T) {
	want := []Event{
		EventIssueReceived, EventInfoMissing, EventClarificationCompleted,
		EventInfoSufficient, EventApprovalRequired, EventScopePermitted,
		EventOwnerApproved, EventApprovalDenied, EventClaimed,
		EventDevelopmentStarted, EventClaimStopped, EventDevelopmentStopped,
		EventCandidateSubmitted, EventQAFailed, EventQAPassed, EventRolloutStarted, EventRolloutHealthy,
		EventErrorThresholdExceeded, EventPostDeployVerified,
		EventRegressionDetected, EventIssueClosed, EventRecoveryReported,
		EventRetryAuthorized,
	}
	got := Events()
	if !slices.Equal(got, want) {
		t.Fatalf("Events() = %v, want %v", got, want)
	}
	seen := map[Event]bool{}
	for _, e := range got {
		if e == "" || seen[e] {
			t.Errorf("event %q is empty or repeated", e)
		}
		seen[e] = true
		if !e.Valid() {
			t.Errorf("event %q is listed but not valid", e)
		}
	}
}

func TestStatesAndEventsListsCannotBeMutatedByCallers(t *testing.T) {
	States()[0] = "Tampered"
	Events()[0] = "tampered"
	if States()[0] != StateIntake || Events()[0] != EventIssueReceived {
		t.Fatal("a caller changed the shared state or event list")
	}
}

func TestUnknownStatesAndEventsAreRejected(t *testing.T) {
	for _, s := range []State{"", "Releasing", "intake", "Done"} {
		if s.Valid() {
			t.Errorf("state %q must not be valid", s)
		}
		if _, err := ParseState(string(s)); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("ParseState(%q) error code = %q, want %q", s, CodeOf(err), CodeInvalidArgument)
		}
	}
	for _, e := range []Event{"", "merged", "QA_PASSED"} {
		if e.Valid() {
			t.Errorf("event %q must not be valid", e)
		}
		if _, err := ParseEvent(string(e)); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("ParseEvent(%q) error code = %q, want %q", e, CodeOf(err), CodeInvalidArgument)
		}
	}
}

func TestParseStateAndEventAcceptEveryListedValue(t *testing.T) {
	for _, s := range States() {
		if got, err := ParseState(string(s)); err != nil || got != s {
			t.Errorf("ParseState(%q) = %q, %v", s, got, err)
		}
	}
	for _, e := range Events() {
		if got, err := ParseEvent(string(e)); err != nil || got != e {
			t.Errorf("ParseEvent(%q) = %q, %v", e, got, err)
		}
	}
}
