package acceptance

import (
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
)

// A step-up counts only while it is fresh: an Owner whose confirmation of
// a request went stale is sent to confirm it again, and the decision then
// goes through.
func TestAStaleStepUpIsRefusedAndAFreshOneDecides(t *testing.T) {
	f := harness.New(t, harness.Options{})
	owner := f.SignInOwner()
	c := newClients(t, f, owner)
	if got := c.confirmed(t, "budget.set", call{Input: raw(`{"mode":"unlimited"}`)}); got.Code != core.CodeApprovalRequired {
		t.Fatalf("plan: %q %s", got.Code, got.Message)
	}
	req := openRequest(t, c, "budget.set")

	owner.StepUpFor("approvals.decide", req.ID)
	f.Clock.Advance(6 * time.Minute)
	got := c.on(t, surfaceDashboard, "approvals.decide", call{Input: decideInput(req)})
	if got.Code != core.CodeStepUpRequired {
		t.Fatalf("a stale step-up decided: %q %s", got.Code, got.Message)
	}
	if got.Details["step_up_action"] != "approvals.decide" || got.Details["step_up_target"] != req.ID {
		t.Fatalf("the refusal offers a step-up for %+v, want this request", got.Details)
	}
	owner.StepUpFor("approvals.decide", req.ID)
	if got := c.on(t, surfaceDashboard, "approvals.decide", call{Input: decideInput(req)}); got.Code != "" {
		t.Fatalf("a fresh step-up could not decide: %q %s", got.Code, got.Message)
	}
}

// Writes are limited per person across every surface; reads are not, and
// the limit lifts when its window has passed.
func TestWritesBeyondTheLimitAreRefusedOnEverySurfaceAndReadsAreNot(t *testing.T) {
	f := harness.New(t, harness.Options{})
	c := newClients(t, f, f.SignInOwner())
	stop := call{Input: raw(`{"reason":"rate limit check"}`)}
	for i := range actions.DefaultWriteRate {
		if got := c.on(t, surfaceREST, "factory.emergency_stop", stop); got.Code != "" {
			t.Fatalf("write %d answered %q: %s", i+1, got.Code, got.Message)
		}
	}
	for _, surface := range []string{surfaceMCP, surfaceWebMCP, surfaceDashboard} {
		got := c.on(t, surface, "factory.emergency_stop", stop)
		if got.Code != core.CodeRateLimited {
			t.Fatalf("%s ran a write beyond the limit: %q", surface, got.Code)
		}
	}
	if got := c.on(t, surfaceMCP, "factory.status", call{Input: raw(`{}`)}); got.Code != "" {
		t.Fatalf("a read was limited: %q", got.Code)
	}
	if stops, _ := f.Supervisor.Recorded(); len(stops) != actions.DefaultWriteRate {
		t.Fatalf("the supervisor was asked to stop %d times, want %d", len(stops), actions.DefaultWriteRate)
	}
	f.Clock.Advance(actions.DefaultWriteRateWindow)
	if got := c.on(t, surfaceREST, "factory.emergency_stop", stop); got.Code != "" {
		t.Fatalf("the limit did not lift after its window: %q", got.Code)
	}
}
