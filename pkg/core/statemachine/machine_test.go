package statemachine

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// countingGuards records how often each guard was consulted.
type countingGuards struct{ delegation, approval, claim, attempts, qa, postDeploy, retry int }

func (c *countingGuards) guards() Guards {
	return Guards{
		Delegation: DelegationGuardFunc(func(_ context.Context, t core.Transition) (DelegationProof, error) {
			c.delegation++
			return validDelegation(t), nil
		}),
		Approval: ApprovalGuardFunc(func(_ context.Context, t core.Transition) (ApprovalProof, error) {
			c.approval++
			return validApproval(t), nil
		}),
		Claim: ClaimGuardFunc(func(_ context.Context, t core.Transition) (ClaimProof, error) {
			c.claim++
			return validClaim(t), nil
		}),
		Attempts: AttemptsGuardFunc(func(_ context.Context, t core.Transition) (AttemptsProof, error) {
			c.attempts++
			return validAttempts(t), nil
		}),
		QA: QAGuardFunc(func(context.Context, core.Transition) (QAProof, error) {
			c.qa++
			return validQA(), nil
		}),
		PostDeploy: PostDeployGuardFunc(func(context.Context, core.Transition) (PostDeployProof, error) {
			c.postDeploy++
			return validPostDeploy(), nil
		}),
		Retry: RetryGuardFunc(func(context.Context, core.Transition) (core.AuthzDecision, error) {
			c.retry++
			return allowed(), nil
		}),
	}
}

func (c *countingGuards) total() int {
	return c.delegation + c.approval + c.claim + c.attempts + c.qa + c.postDeploy + c.retry
}

// guardedRequests is one well-formed request per guarded step.
func guardedRequests() map[GuardKind]core.Transition {
	return map[GuardKind]core.Transition{
		GuardDelegation: delegatedScopeRequest(),
		GuardApproval:   ownerApprovedRequest(),
		GuardClaim:      claimRequest(),
		GuardAttempts:   qaRetryRequest(),
		GuardQAPass:     qaPassRequest(),
		GuardPostDeploy: postDeployRequest(),
		GuardRetry:      retryRequest(),
	}
}

func TestEveryGuardKindOfTheTableHasARequestInTheseTests(t *testing.T) {
	requests := guardedRequests()
	for _, e := range Edges() {
		if e.Guard == GuardNone {
			continue
		}
		request, ok := requests[e.Guard]
		if !ok || request.Event != e.Event {
			t.Errorf("guard %s on %s is not exercised by a request of its own", e.Guard, e.Event)
		}
	}
}

func TestEachGuardedStepConsultsOnlyItsOwnGuard(t *testing.T) {
	want := map[GuardKind]countingGuards{
		GuardDelegation: {delegation: 1},
		GuardApproval:   {approval: 1},
		GuardClaim:      {claim: 1},
		GuardAttempts:   {attempts: 1},
		GuardQAPass:     {qa: 1},
		GuardPostDeploy: {postDeploy: 1},
		GuardRetry:      {retry: 1},
	}
	for guard, request := range guardedRequests() {
		var calls countingGuards
		if err := machineWith(calls.guards()).Check(context.Background(), request); err != nil {
			t.Errorf("%s: refused with every guard satisfied: %v", guard, err)
		}
		if calls != want[guard] {
			t.Errorf("%s: guards consulted %+v, want %+v", guard, calls, want[guard])
		}
	}
}

func TestUnguardedStepsConsultNoGuard(t *testing.T) {
	var calls countingGuards
	machine := machineWith(calls.guards())
	for _, e := range Edges() {
		if e.Guard != GuardNone {
			continue
		}
		if err := machine.Check(context.Background(), transitionOn(e.From, e.Event, e.To)); err != nil {
			t.Errorf("unguarded step on %s refused: %v", e.Event, err)
		}
	}
	if calls.total() != 0 {
		t.Errorf("unguarded steps consulted guards: %+v", calls)
	}
}

func TestUnguardedStepsNeedNoGuardsWired(t *testing.T) {
	machine := machineWith(Guards{})
	for _, e := range Edges() {
		if e.Guard != GuardNone {
			continue
		}
		if err := machine.Check(context.Background(), transitionOn(e.From, e.Event, e.To)); err != nil {
			t.Errorf("unguarded step on %s refused without guards: %v", e.Event, err)
		}
	}
}

func TestIllegalStepsAreRefusedBeforeAnyGuardRuns(t *testing.T) {
	var calls countingGuards
	machine := machineWith(calls.guards())
	illegal := []core.Transition{
		transitionOn(core.StateIntake, core.EventClaimed, core.StateClaimed),
		transitionOn(core.StateDeveloping, core.EventQAPassed, core.StateReleaseCandidate),
		transitionOn(core.StateCanary, core.EventPostDeployVerified, core.StateVerified),
		transitionOn(core.StateRolledBack, core.EventRetryAuthorized, core.StateReady),
		transitionOn(core.StateReady, core.EventClaimed, core.StateDeveloping),
		transitionOn(core.StateIntake, core.EventScopePermitted, core.StateReady),
		transitionOn(core.StateScopeDraft, core.EventOwnerApproved, core.StateReady),
		transitionOn(core.StateDeveloping, core.EventQAFailed, core.StateDeveloping),
	}
	for _, request := range illegal {
		err := machine.Check(context.Background(), request)
		if !core.HasCode(err, core.CodeInvalidTransition) {
			t.Errorf("%s on %s to %s: want %s, got %v", request.From, request.Event, request.To, core.CodeInvalidTransition, err)
		}
	}
	if calls.total() != 0 {
		t.Errorf("illegal steps consulted guards: %+v", calls)
	}
}

func TestGuardedStepsFailClosedWhenNoGuardIsWired(t *testing.T) {
	machine := machineWith(Guards{})
	cases := guardedRequests()
	if len(cases) != 7 {
		t.Fatalf("%d guarded steps are exercised, want 7", len(cases))
	}
	for guard, request := range cases {
		requireGuardFailed(t, string(guard), machine.Check(context.Background(), request), guard)
	}
}

func TestMalformedRequestsAreRefusedBeforeAnyGuardRuns(t *testing.T) {
	cases := map[string]func(*core.Transition){
		"no idempotency key":   func(r *core.Transition) { r.IdempotencyKey = "" },
		"negative version":     func(r *core.Transition) { r.ExpectedVersion = -1 },
		"negative attempt":     func(r *core.Transition) { r.Attempt = -1 },
		"malformed run id":     func(r *core.Transition) { r.RunID = "Run 1" },
		"token without owner":  func(r *core.Transition) { r.LeaseOwner = "" },
		"negative fencing":     func(r *core.Transition) { r.FencingToken = -1 },
		"issue without number": func(r *core.Transition) { r.IssueID.Number = 0 },
	}
	for name, mutate := range cases {
		var calls countingGuards
		request := claimRequest()
		mutate(&request)
		err := machineWith(calls.guards()).Check(context.Background(), request)
		if !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, err)
		}
		if calls.total() != 0 {
			t.Errorf("%s: a malformed request reached a guard: %+v", name, calls)
		}
	}
}

func TestCreationMustStartAtVersionZero(t *testing.T) {
	request := transitionOn("", core.EventIssueReceived, core.StateIntake)
	request.ExpectedVersion = 3
	err := machineWith(Guards{}).Check(context.Background(), request)
	if !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("creating a record over version 3: want %s, got %v", core.CodeInvalidArgument, err)
	}
}

func TestGuardsSeeTheRequestAndItsContext(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "trace-1")
	request := retryRequest()
	seen := false
	guards := Guards{Retry: RetryGuardFunc(func(got context.Context, t core.Transition) (core.AuthzDecision, error) {
		seen = got.Value(key{}) == "trace-1" && t.ActorID == request.ActorID && t.IssueID == request.IssueID
		return allowed(), nil
	})}
	if err := machineWith(guards).Check(ctx, request); err != nil {
		t.Fatalf("authorized retry refused: %v", err)
	}
	if !seen {
		t.Fatal("the guard did not receive the caller's context and request")
	}
}

func TestAMachineWithoutAClockReadsTheWallClock(t *testing.T) {
	live := func(t core.Transition) ClaimProof {
		proof := validClaim(t)
		proof.LeaseExpiresAt = time.Now().Add(time.Hour)
		return proof
	}
	lapsed := func(t core.Transition) ClaimProof {
		proof := validClaim(t)
		proof.LeaseExpiresAt = time.Now().Add(-time.Hour)
		return proof
	}
	if err := New(nil, Guards{Claim: claimWith(live)}).Check(context.Background(), claimRequest()); err != nil {
		t.Errorf("a lease valid for another hour was refused: %v", err)
	}
	err := New(nil, Guards{Claim: claimWith(lapsed)}).Check(context.Background(), claimRequest())
	requireGuardFailed(t, "lapsed lease", err, GuardClaim)
}
