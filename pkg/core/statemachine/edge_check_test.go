package statemachine

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

func TestEveryDiagramEdgeIsAStepTheLifecycleContains(t *testing.T) {
	t.Parallel()
	for _, e := range briefEdges {
		if err := CheckEdge(transitionOn(e.from, e.event, e.to)); err != nil {
			t.Errorf("%q --%s--> %s is in the lifecycle but was refused: %v", e.from, e.event, e.to, err)
		}
	}
}

func TestAnIssueCannotJumpFromIntakeToClosed(t *testing.T) {
	t.Parallel()
	for _, event := range core.Events() {
		err := CheckEdge(transitionOn(core.StateIntake, event, core.StateClosed))
		if !core.HasCode(err, core.CodeInvalidTransition) {
			t.Errorf("Intake --%s--> Closed: want %s, got %v", event, core.CodeInvalidTransition, err)
		}
	}
	// A shortcut stays refused whatever else the request carries: the version,
	// the source state and the fencing token of a stored record say nothing
	// about whether the lifecycle has the step.
	shortcut := core.Transition{
		IssueID:         testIssue,
		From:            core.StateIntake,
		To:              core.StateClosed,
		Event:           core.EventIssueClosed,
		ExpectedVersion: 1,
		IdempotencyKey:  "close:early",
		LeaseOwner:      "dispatcher-1",
		FencingToken:    5,
	}
	if err := shortcut.Validate(); err != nil {
		t.Fatalf("the shortcut must be a well-formed request to prove the table refuses it: %v", err)
	}
	err := CheckEdge(shortcut)
	if !core.HasCode(err, core.CodeInvalidTransition) {
		t.Fatalf("a well-formed Intake to Closed write: want %s, got %v", core.CodeInvalidTransition, err)
	}
	if details := detailsOf(err); details["from"] != "Intake" || details["to"] != "Closed" {
		t.Errorf("the refusal does not name the step that was asked for: %v", details)
	}
}

func TestOnlyDiagramEdgesPassTheEdgeCheckAcrossEverySourceTargetAndEvent(t *testing.T) {
	t.Parallel()
	want := map[briefEdge]bool{}
	for _, e := range briefEdges {
		want[e] = true
	}
	tried, accepted := 0, 0
	for _, from := range sourceStates() {
		for _, to := range append([]core.State{"", "Releasing"}, core.States()...) {
			for _, event := range append([]core.Event{"", "merged"}, core.Events()...) {
				tried++
				err := CheckEdge(transitionOn(from, event, to))
				if want[briefEdge{from, event, to}] {
					accepted++
					if err != nil {
						t.Errorf("%q --%s--> %s was refused: %v", from, event, to, err)
					}
					continue
				}
				if !core.HasCode(err, core.CodeInvalidTransition) {
					t.Errorf("%q --%q--> %q: want %s, got %v", from, event, to, core.CodeInvalidTransition, err)
				}
			}
		}
	}
	if tried != 16*17*26 || accepted != 24 {
		t.Fatalf("tried %d steps and accepted %d, want 7072 tried and 24 accepted", tried, accepted)
	}
}

func TestTheEdgeCheckAgreesWithNextAndWithTheMachine(t *testing.T) {
	t.Parallel()
	machine := machineWith(passingGuards())
	for _, from := range sourceStates() {
		for _, to := range core.States() {
			for _, event := range core.Events() {
				request := transitionOn(from, event, to)
				next, lookupErr := Next(from, event)
				inTable := lookupErr == nil && next == to
				if got := CheckEdge(request) == nil; got != inTable {
					t.Errorf("%q --%s--> %s: CheckEdge accepts=%v, Next says %v", from, event, to, got, inTable)
				}
				refusedAsIllegal := core.HasCode(machine.Check(context.Background(), request), core.CodeInvalidTransition)
				if refusedAsIllegal == inTable {
					t.Errorf("%q --%s--> %s: the machine and the edge check disagree", from, event, to)
				}
			}
		}
	}
}

func TestTheEdgeCheckJudgesTheStepAndNothingElse(t *testing.T) {
	t.Parallel()
	// The check is a table lookup: it reads no clock, consults no guard and
	// does not judge the rest of the request, which is the caller's to check.
	guarded := claimRequest()
	guarded.IdempotencyKey = ""
	guarded.LeaseOwner, guarded.FencingToken = "", 0
	if err := CheckEdge(guarded); err != nil {
		t.Fatalf("a legal step was refused for something other than its edge: %v", err)
	}
	if err := CheckEdge(core.Transition{}); !core.HasCode(err, core.CodeInvalidTransition) {
		t.Fatalf("an empty request: want %s, got %v", core.CodeInvalidTransition, err)
	}
}
