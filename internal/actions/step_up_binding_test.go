package actions_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// An approved execution spends the confirmation the person made for that
// approval: one confirmation runs one call, and a confirmation for one
// approval runs nothing under another.
func TestAStepUpForOneApprovalRunsThatApprovalOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.widgets.mu.Lock()
	f.widgets.names["w2"] = "beta"
	f.widgets.mu.Unlock()
	idA := f.plan(t, f.maintainer(), "widgets.purge", purgeW1)
	idB := f.plan(t, f.maintainer(), "widgets.purge", `{"id":"w2"}`)
	f.approve(t, idA, approvals.DecisionApprove)
	f.approve(t, idB, approvals.DecisionApprove)

	f.stepUps.confirm(f.maintainer(), "widgets.purge", idA)
	_, err := f.execute(f.maintainer(), "widgets.purge", `{"id":"w2"}`, idB)
	requireCode(t, err, core.CodeStepUpRequired)
	f.stepUps.confirm(f.owner(), "widgets.purge", idA)
	_, err = f.execute(f.owner(), "keys.rotate", purgeW1, idA)
	requireCode(t, err, core.CodeStepUpRequired)
	if _, err := f.execute(f.maintainer(), "widgets.purge", purgeW1, idA); err != nil {
		t.Fatalf("the confirmed call: %v", err)
	}
	if f.applied() != 1 {
		t.Fatalf("applied %d changes, want 1", f.applied())
	}
	f.stepUps.confirm(f.maintainer(), "widgets.purge", idA)
	_, err = f.execute(f.maintainer(), "widgets.purge", purgeW1, idA)
	requireCode(t, err, core.CodeReplayed)
}

// A plan of an action that needs step-up is confirmed for its exact input,
// named in the refusal so the dashboard can ask for it.
func TestAPlanIsConfirmedForItsExactInput(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.stepUps.setStrict()
	_, err := f.svc.Invoke(context.Background(), "widgets.purge", core.ActionRequest{Actor: f.maintainer(), Input: input(purgeW1)})
	typed := asError(t, err, core.CodeStepUpRequired)
	target := typed.Details["step_up_target"]
	if typed.Details["step_up_action"] != "widgets.purge" || len(target) != len("sha256:")+64 {
		t.Fatalf("the refusal names %+v", typed.Details)
	}
	f.stepUps.confirm(f.maintainer(), "widgets.purge", target)
	_, err = f.svc.Invoke(context.Background(), "widgets.purge", core.ActionRequest{Actor: f.maintainer(), Input: input(`{"id":"w2"}`)})
	requireCode(t, err, core.CodeStepUpRequired)
	if id := f.plan(t, f.maintainer(), "widgets.purge", ` { "id" : "w1" } `); id == "" {
		t.Fatal("the confirmed plan filed nothing")
	}
}

// Step-up actions run only from the dashboard's form posts: a session used
// through REST, which is what WebMCP and any script in the page call, a
// token on any surface and an agent are refused and pointed at the
// dashboard, whatever step-up time the caller presents.
func TestAStepUpIsAcceptedOnlyFromTheDashboard(t *testing.T) {
	t.Parallel()
	for _, surface := range []core.Surface{core.SurfaceAPI, core.SurfaceCLI, core.SurfaceMCP, core.SurfaceWebMCP} {
		t.Run(string(surface), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.svc = rebuiltWithStepUpPage(t, f)
			id := f.plan(t, f.maintainer(), "widgets.purge", purgeW1)
			f.approve(t, id, approvals.DecisionApprove)
			actor := f.maintainer()
			actor.Surface, actor.StepUpAt = surface, f.clock.Now()
			f.stepUps.confirm(actor, "widgets.purge", id)
			asked := len(f.stepUps.asked)
			_, err := f.execute(actor, "widgets.purge", purgeW1, id)
			typed := asError(t, err, core.CodeStepUpRequired)
			if typed.NextActions[0].Params["dashboard"] != "/approvals" {
				t.Fatalf("the refusal does not point at the dashboard: %+v", typed.NextActions)
			}
			if f.applied() != 0 || len(f.stepUps.asked) != asked {
				t.Fatalf("applied %d, asked for %v", f.applied(), f.stepUps.asked[asked:])
			}
			denied := f.audit.Find("widgets.purge", core.OutcomeDenied)
			if len(denied) == 0 || denied[len(denied)-1].Surface != surface {
				t.Fatalf("the refusal is not audited with its surface: %+v", denied)
			}
		})
	}
}

// A step-up time the caller brings is ignored: only a spent confirmation
// sets it.
func TestAPresentedStepUpTimeIsIgnored(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.plan(t, f.maintainer(), "widgets.purge", purgeW1)
	f.approve(t, id, approvals.DecisionApprove)
	f.stepUps.setStrict()
	actor := f.maintainer()
	actor.StepUpAt = f.clock.Now().Add(-time.Second)
	_, err := f.execute(actor, "widgets.purge", purgeW1, id)
	requireCode(t, err, core.CodeStepUpRequired)
	if f.applied() != 0 {
		t.Fatal("a presented step-up time ran the action")
	}
}

// A caller who may not run the action at all is refused as such, and the
// confirmation it holds stays unspent.
func TestADeniedCallerSpendsNoConfirmation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.plan(t, f.owner(), "keys.rotate", purgeW1)
	f.approve(t, id, approvals.DecisionApprove)
	f.stepUps.confirm(f.maintainer(), "keys.rotate", id)
	_, err := f.execute(f.maintainer(), "keys.rotate", purgeW1, id)
	requireCode(t, err, core.CodePermissionDenied)
	if n := f.stepUps.confirmed[[3]string{"ses_maint", "keys.rotate", id}]; n != 1 {
		t.Fatalf("the denied call spent its confirmation: %d left", n)
	}
}
