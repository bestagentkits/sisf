package statemachine

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/internal/core"
)

// checkRetryWith runs the step from Blocked back to Ready with the given
// authorization decision, after mutate has changed the request.
func checkRetryWith(decision core.AuthzDecision, mutate func(*core.Transition)) error {
	request := retryRequest()
	mutate(&request)
	return machineWith(Guards{Retry: retryWith(decision)}).Check(context.Background(), request)
}

func unchanged(*core.Transition) {}

func TestABlockedIssueReturnsToReadyOnAnAuthorizedRetry(t *testing.T) {
	if err := checkRetryWith(allowed(), unchanged); err != nil {
		t.Fatalf("an authorized retry was refused: %v", err)
	}
}

func TestARetryThatIsNotAuthorizedLeavesTheIssueBlocked(t *testing.T) {
	cases := map[string]core.AuthzDecision{
		"no decision was made":          {},
		"the actor is denied":           {Effect: core.EffectDeny, Reason: "reporters cannot retry"},
		"an approval is still required": {Effect: core.EffectRequireApproval},
		"a step-up is still required":   {Effect: core.EffectRequireStepUp},
		"the decision is not a known effect": {
			Effect: "probably",
		},
	}
	for name, decision := range cases {
		requireGuardFailed(t, name, checkRetryWith(decision, unchanged), GuardRetry)
	}
}

func TestARetryMustNameWhoAskedForIt(t *testing.T) {
	err := checkRetryWith(allowed(), func(r *core.Transition) { r.ActorID = "" })
	requireGuardFailed(t, "anonymous retry", err, GuardRetry)

	blank := checkRetryWith(allowed(), func(r *core.Transition) { r.ActorID = "  " })
	requireGuardFailed(t, "blank actor", blank, GuardRetry)
}

func TestARefusedRetryReportsTheAuthorizationOutcome(t *testing.T) {
	decision := core.AuthzDecision{Effect: core.EffectRequireApproval, Reason: "owner approval pending"}
	details := detailsOf(checkRetryWith(decision, unchanged))
	if details["effect"] != string(core.EffectRequireApproval) || details["reason"] != "owner approval pending" {
		t.Fatalf("refusal details = %v, want the authorization effect and reason", details)
	}
}

func TestOnlyBlockedIssuesCanBeRetried(t *testing.T) {
	for _, from := range core.States() {
		if from == core.StateBlocked {
			continue
		}
		request := transitionOn(from, core.EventRetryAuthorized, core.StateReady)
		err := machineWith(passingGuards()).Check(context.Background(), request)
		if !core.HasCode(err, core.CodeInvalidTransition) {
			t.Errorf("an issue in %s was retried: %v", from, err)
		}
	}
}
