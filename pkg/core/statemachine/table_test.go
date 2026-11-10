package statemachine

import (
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

func TestLifecycleTableHoldsExactlyTheDiagramEdges(t *testing.T) {
	got := Edges()
	if len(got) != len(briefEdges) {
		t.Fatalf("table has %d edges, the lifecycle has %d", len(got), len(briefEdges))
	}
	for _, want := range briefEdges {
		found := slices.ContainsFunc(got, func(e Edge) bool {
			return e.From == want.from && e.Event == want.event && e.To == want.to
		})
		if !found {
			t.Errorf("edge %q --%s--> %q is missing from the table", want.from, want.event, want.to)
		}
	}
}

func TestEveryStatePairIsConnectedOnlyWhereTheDiagramConnectsIt(t *testing.T) {
	type pair struct{ from, to core.State }
	want := map[pair]bool{}
	for _, e := range briefEdges {
		want[pair{e.from, e.to}] = true
	}
	got := map[pair]int{}
	for _, e := range Edges() {
		got[pair{e.From, e.To}]++
	}
	pairs := 0
	for _, from := range core.States() {
		for _, to := range core.States() {
			pairs++
			count := got[pair{from, to}]
			switch {
			case want[pair{from, to}] && count != 1:
				t.Errorf("%s -> %s must have exactly one edge, has %d", from, to, count)
			case !want[pair{from, to}] && count != 0:
				t.Errorf("%s -> %s is not in the lifecycle but has %d edge(s)", from, to, count)
			}
		}
	}
	if pairs != 15*15 {
		t.Fatalf("checked %d state pairs, want 225", pairs)
	}
}

func TestEveryEventDrivesExactlyOneEdge(t *testing.T) {
	uses := map[core.Event]int{}
	for _, e := range Edges() {
		uses[e.Event]++
	}
	for _, event := range core.Events() {
		if uses[event] != 1 {
			t.Errorf("event %s drives %d edges, want exactly 1", event, uses[event])
		}
	}
	if len(uses) != len(core.Events()) {
		t.Errorf("table uses %d distinct events, the lifecycle defines %d", len(uses), len(core.Events()))
	}
}

func TestEdgesOnlyConnectTheFifteenStates(t *testing.T) {
	for _, e := range Edges() {
		if e.From != "" && !e.From.Valid() {
			t.Errorf("edge on %s leaves unknown state %q", e.Event, e.From)
		}
		if !e.To.Valid() {
			t.Errorf("edge on %s enters unknown state %q", e.Event, e.To)
		}
	}
}

func TestOnlyTheSevenGuardedStepsCarryAGuard(t *testing.T) {
	want := map[core.Event]GuardKind{
		core.EventScopePermitted:     GuardDelegation,
		core.EventOwnerApproved:      GuardApproval,
		core.EventClaimed:            GuardClaim,
		core.EventQAFailed:           GuardAttempts,
		core.EventQAPassed:           GuardQAPass,
		core.EventPostDeployVerified: GuardPostDeploy,
		core.EventRetryAuthorized:    GuardRetry,
	}
	for _, e := range Edges() {
		if e.Guard != want[e.Event] {
			t.Errorf("edge on %s has guard %q, want %q", e.Event, e.Guard, want[e.Event])
		}
	}
}

func TestEveryWayIntoReadyIsGuarded(t *testing.T) {
	into := 0
	for _, e := range Edges() {
		if e.To != core.StateReady {
			continue
		}
		into++
		if e.Guard == GuardNone {
			t.Errorf("%s reaches Ready on %s without any proof", e.From, e.Event)
		}
	}
	if into != 3 {
		t.Fatalf("%d edges enter Ready, want the delegated, the approved and the retried one", into)
	}
}

func TestEveryStepThatClosesALoopIsGuarded(t *testing.T) {
	// A step back to a state the issue has already been through is how work
	// repeats, so each one needs a proof that bounds or authorizes it.
	position := map[core.State]int{}
	for i, state := range core.States() {
		position[state] = i
	}
	loops := 0
	for _, e := range Edges() {
		if e.From == "" || position[e.To] >= position[e.From] {
			continue
		}
		loops++
		if e.Guard == GuardNone {
			t.Errorf("%s returns to %s on %s without any proof", e.From, e.To, e.Event)
		}
	}
	if loops != 2 {
		t.Fatalf("%d edges lead back to an earlier state, want the QA retry and the authorized retry", loops)
	}
}

func TestClosedIsTerminal(t *testing.T) {
	for _, e := range Edges() {
		if e.From == core.StateClosed {
			t.Errorf("Closed must have no way out, found edge on %s to %s", e.Event, e.To)
		}
	}
	if out := eventsFrom(core.StateClosed); len(out) != 0 {
		t.Errorf("Closed accepts events %v", out)
	}
}

func TestEdgeListCannotBeMutatedByCallers(t *testing.T) {
	Edges()[0] = Edge{From: core.StateIntake, Event: core.EventIssueClosed, To: core.StateClosed}
	if next, err := Next(core.StateIntake, core.EventIssueClosed); err == nil {
		t.Fatalf("a caller added an edge: Intake now closes into %s", next)
	}
	if first := Edges()[0]; first.From != "" || first.To != core.StateIntake {
		t.Fatalf("a caller changed the shared edge list: first edge is now %+v", first)
	}
}

func TestStatesOutsideTheLifecycleHaveNoEdges(t *testing.T) {
	for _, from := range []core.State{"Releasing", "Done", "intake", "CLOSED", " "} {
		for _, event := range core.Events() {
			next, err := Next(from, event)
			if !core.HasCode(err, core.CodeInvalidTransition) || next != "" {
				t.Errorf("Next(%q, %s) = %q, %v; want %s", from, event, next, err, core.CodeInvalidTransition)
			}
		}
	}
}

func TestEventsOutsideTheLifecycleMoveNothing(t *testing.T) {
	for _, from := range sourceStates() {
		for _, event := range []core.Event{"", "merged", "QA_PASSED", "released"} {
			next, err := Next(from, event)
			if !core.HasCode(err, core.CodeInvalidTransition) || next != "" {
				t.Errorf("Next(%q, %q) = %q, %v; want %s", from, event, next, err, core.CodeInvalidTransition)
			}
		}
	}
}

func TestRejectedEventListsTheEventsTheStateAccepts(t *testing.T) {
	_, err := Next(core.StateIntake, core.EventIssueClosed)
	details := detailsOf(err)
	if details["from"] != "Intake" || details["event"] != "issue_closed" {
		t.Errorf("rejection details = %v, want the state and event that were refused", details)
	}
	if details["allowed_events"] != "info_missing,info_sufficient" {
		t.Errorf("allowed_events = %q, want the two events Intake accepts", details["allowed_events"])
	}
}
