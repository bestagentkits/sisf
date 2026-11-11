package statemachine

import (
	"errors"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// testNow is the instant every test clock reports.
var testNow = time.Date(2026, 11, 10, 7, 0, 0, 0, time.UTC)

var (
	testIssue  = core.IssueRef{Owner: "acme", Repo: "app", Number: 7}
	otherIssue = core.IssueRef{Owner: "acme", Repo: "app", Number: 8}
)

const (
	// testFactory is the factory deployment the machine under test serves.
	testFactory  core.FactoryID     = "acme-factory"
	otherFactory core.FactoryID     = "other-factory"
	testRun      core.RunID         = "run-0001"
	otherRun     core.RunID         = "run-0002"
	testSHA      core.CommitSHA     = "0123456789abcdef0123456789abcdef01234567"
	otherSHA     core.CommitSHA     = "fedcba9876543210fedcba9876543210fedcba98"
	testDigest   core.Digest        = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	released     core.ReleaseDigest = core.ReleaseDigest(testDigest)
	otherBuild   core.ReleaseDigest = "sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	// scopeDigest identifies the scope of work attached to the test issue.
	scopeDigest core.Digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	// testMaxAttempts is the attempt limit the test policy states.
	testMaxAttempts = 3
)

// fixedClock never moves, so expiry rules can be driven to the exact instant.
type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

func (c fixedClock) After(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

// briefEdge is one arrow of the lifecycle diagram, written out here
// independently of the table under test.
type briefEdge struct {
	from  core.State
	event core.Event
	to    core.State
}

// briefEdges lists the creation arrow and the twenty-two arrows between states.
// Anything absent from this list must be rejected.
var briefEdges = []briefEdge{
	{"", core.EventIssueReceived, core.StateIntake},
	{core.StateIntake, core.EventInfoMissing, core.StateClarifying},
	{core.StateClarifying, core.EventClarificationCompleted, core.StateScopeDraft},
	{core.StateIntake, core.EventInfoSufficient, core.StateScopeDraft},
	{core.StateScopeDraft, core.EventApprovalRequired, core.StateApprovalPending},
	{core.StateScopeDraft, core.EventScopePermitted, core.StateReady},
	{core.StateApprovalPending, core.EventOwnerApproved, core.StateReady},
	{core.StateApprovalPending, core.EventApprovalDenied, core.StateBlocked},
	{core.StateReady, core.EventClaimed, core.StateClaimed},
	{core.StateClaimed, core.EventDevelopmentStarted, core.StateDeveloping},
	{core.StateClaimed, core.EventClaimStopped, core.StateBlocked},
	{core.StateDeveloping, core.EventDevelopmentStopped, core.StateBlocked},
	{core.StateDeveloping, core.EventCandidateSubmitted, core.StateQAEvaluation},
	{core.StateQAEvaluation, core.EventQAFailed, core.StateDeveloping},
	{core.StateQAEvaluation, core.EventQAPassed, core.StateReleaseCandidate},
	{core.StateReleaseCandidate, core.EventRolloutStarted, core.StateCanary},
	{core.StateCanary, core.EventRolloutHealthy, core.StateMonitoring},
	{core.StateCanary, core.EventErrorThresholdExceeded, core.StateRolledBack},
	{core.StateMonitoring, core.EventPostDeployVerified, core.StateVerified},
	{core.StateMonitoring, core.EventRegressionDetected, core.StateRolledBack},
	{core.StateVerified, core.EventIssueClosed, core.StateClosed},
	{core.StateRolledBack, core.EventRecoveryReported, core.StateBlocked},
	{core.StateBlocked, core.EventRetryAuthorized, core.StateReady},
}

// sourceStates is every place an issue can be before a transition: not yet
// recorded, or in one of the fifteen states.
func sourceStates() []core.State {
	return append([]core.State{""}, core.States()...)
}

// transitionOn builds a structurally valid request for one step. It is leased
// and names a run, an actor, the candidate and the release, so every guard has
// what it may bind to. A failed evaluation starts the second attempt.
func transitionOn(from core.State, event core.Event, to core.State) core.Transition {
	t := core.Transition{
		IssueID:         testIssue,
		From:            from,
		To:              to,
		Event:           event,
		ExpectedVersion: 1,
		IdempotencyKey:  "step:" + string(event),
		LeaseOwner:      "dispatcher-1",
		FencingToken:    5,
		RunID:           testRun,
		Attempt:         1,
		ActorID:         "user:owner-1",
		CandidateDigest: testDigest,
		ReleaseDigest:   released,
	}
	if from == "" {
		t.ExpectedVersion = 0
	}
	if event == core.EventQAFailed {
		t.Attempt = 2
	}
	return t
}

// Requests for the ten guarded steps.
func delegatedScopeRequest() core.Transition {
	return transitionOn(core.StateScopeDraft, core.EventScopePermitted, core.StateReady)
}

func ownerApprovedRequest() core.Transition {
	return transitionOn(core.StateApprovalPending, core.EventOwnerApproved, core.StateReady)
}

func claimRequest() core.Transition {
	return transitionOn(core.StateReady, core.EventClaimed, core.StateClaimed)
}

func candidateRequest() core.Transition {
	return transitionOn(core.StateDeveloping, core.EventCandidateSubmitted, core.StateQAEvaluation)
}

func qaRetryRequest() core.Transition {
	return transitionOn(core.StateQAEvaluation, core.EventQAFailed, core.StateDeveloping)
}

func qaPassRequest() core.Transition {
	return transitionOn(core.StateQAEvaluation, core.EventQAPassed, core.StateReleaseCandidate)
}

func rolloutRequest() core.Transition {
	return transitionOn(core.StateReleaseCandidate, core.EventRolloutStarted, core.StateCanary)
}

func canaryRequest() core.Transition {
	return transitionOn(core.StateCanary, core.EventRolloutHealthy, core.StateMonitoring)
}

func postDeployRequest() core.Transition {
	return transitionOn(core.StateMonitoring, core.EventPostDeployVerified, core.StateVerified)
}

func retryRequest() core.Transition {
	return transitionOn(core.StateBlocked, core.EventRetryAuthorized, core.StateReady)
}

// machineWith builds a machine on the fixed test clock.
func machineWith(guards Guards) *Machine { return New(fixedClock{testNow}, guards) }

// requireHold asserts that err holds a release back on the named guard: the
// step is refused with RELEASE_HOLD, never let through.
func requireHold(t *testing.T, name string, err error, guard GuardKind) {
	t.Helper()
	if !core.HasCode(err, core.CodeReleaseHold) {
		t.Errorf("%s: want %s, got %v", name, core.CodeReleaseHold, err)
		return
	}
	if got := detailsOf(err)["guard"]; got != string(guard) {
		t.Errorf("%s: hold names guard %q, want %q", name, got, guard)
	}
}

// requireGuardFailed asserts that err refuses a step because the named guard
// does not hold.
func requireGuardFailed(t *testing.T, name string, err error, guard GuardKind) {
	t.Helper()
	if !core.HasCode(err, core.CodeGuardFailed) {
		t.Errorf("%s: want %s, got %v", name, core.CodeGuardFailed, err)
		return
	}
	if got := detailsOf(err)["guard"]; got != string(guard) {
		t.Errorf("%s: refusal names guard %q, want %q", name, got, guard)
	}
}

// detailsOf returns the details of a typed error, or nil.
func detailsOf(err error) map[string]string {
	var typed *core.Error
	if !errors.As(err, &typed) || typed == nil {
		return nil
	}
	return typed.Details
}
