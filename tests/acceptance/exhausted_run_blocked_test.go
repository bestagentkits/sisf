package acceptance

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/actions/runactions"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// failedAttempts scripts as many failing coding attempts as a run is
// allowed, each with a detail that must never reach the issue.
func failedAttempts(t *testing.T, f *harness.Factory, number int) {
	t.Helper()
	steps := make([]fake.Step, f.Manifest.Limits.MaxAttempts)
	for i := range steps {
		steps[i] = fake.Fail(0, "the agent printed token=hunter2")
	}
	if err := f.Agent.Script(agent.RoleCoder, f.Issue(number), steps...); err != nil {
		t.Fatal(err)
	}
}

func runStopOf(t *testing.T, f *harness.Factory, run core.RunID) handoff.RunStop {
	t.Helper()
	var stop handoff.RunStop
	err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		stop, err = handoff.ReadRunStop(ctx, tx, run)
		return err
	})
	if err != nil {
		t.Fatalf("read the record of the stopped run: %v", err)
	}
	return stop
}

func ownerActor(f *harness.Factory) core.Actor {
	return core.Actor{ID: "owner:local", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner}, Surface: core.SurfaceCLI, StepUpAt: f.Clock.Now()}
}

// A run that uses every coding attempt it is allowed does not leave its
// issue looking as if it were still worked on: the control zone blocks it,
// GitHub shows it blocked, and the progress comment and runs.explain say
// why with the code of the last failure and how the Owner gets it going
// again, never with the agent's own words. Only the Owner's retry returns
// it to Ready, where a new run claims it; and it does so each time the
// issue is blocked this way, not only the first.
func TestAnExhaustedRunBlocksItsIssueUntilTheOwnerRetriesIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	failedAttempts(t, f, number)
	f.Cycle(6)

	wantSurfaces(t, f, number, core.StateBlocked)
	wantLabels(t, f, number, github.LabelBlocked)
	wantJourneyEnds(t, f, number, []core.State{core.StateClaimed, core.StateDeveloping, core.StateBlocked})
	first := f.Record(number)
	stop := runStopOf(t, f, first.RunID)
	if stop.Cause != statemachine.BlockRunExhausted || stop.LastCode != core.CodeInternal || stop.Attempts != f.Manifest.Limits.MaxAttempts {
		t.Fatalf("the record of the stopped run: %+v", stop)
	}
	retry := "factoryd issue retry --issue " + f.Issue(number).String()
	comment := commentContaining(t, f, number, "<!-- sisf:progress ")
	if !strings.Contains(comment, "**Now:** `Blocked`") || !strings.Contains(comment, "**Blocked:**") ||
		!strings.Contains(comment, "`INTERNAL`") || !strings.Contains(comment, retry) || strings.Contains(comment, "hunter2") {
		t.Errorf("the progress comment does not say why the issue is blocked and how to retry it, in public words:\n%s", comment)
	}
	why := f.Explain(number)
	if !why.Stopped || why.Cause != runactions.CauseBlocked || !strings.Contains(why.Reason, "INTERNAL") ||
		!strings.Contains(why.Reason, retry) || !hasEvidence(why, "run_stop") {
		t.Errorf("runs.explain answers %+v", why)
	}

	// Nothing takes the issue up again by itself, and only the Owner retries it.
	f.Cycle(2)
	if got := f.Record(number); got.State != core.StateBlocked || got.RunID != first.RunID {
		t.Fatalf("the blocked issue moved by itself: %+v", got)
	}
	maintainer := core.Actor{ID: "user:maintainer", Kind: core.ActorHuman, Roles: []core.Role{core.RoleMaintainer}, Surface: core.SurfaceCLI}
	if _, err := f.Control.Retry(ctx, f.Issue(number), maintainer); !core.HasCode(err, core.CodeGuardFailed) {
		t.Fatalf("a maintainer retried the blocked issue: %v", err)
	}
	if _, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f)); err != nil {
		t.Fatalf("the Owner's retry: %v", err)
	}
	if got := f.Record(number).State; got != core.StateReady {
		t.Fatalf("the retried issue is %s, want Ready", got)
	}

	// The new run fails as well: the issue is blocked again, for that run,
	// and the Owner's second retry is not mistaken for the first.
	failedAttempts(t, f, number)
	f.Cycle(6)
	second := f.Record(number)
	if second.State != core.StateBlocked || second.RunID == first.RunID {
		t.Fatalf("the second run left the issue %+v", second)
	}
	if runStopOf(t, f, second.RunID).Cause != statemachine.BlockRunExhausted {
		t.Fatal("the second block has no cause of its own")
	}
	if _, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f)); err != nil {
		t.Fatalf("the Owner's second retry: %v", err)
	}

	// The third run delivers, and the issue closes.
	f.ScriptPatch(number, 0, greetingDiff)
	f.Cycle(8)
	wantSurfaces(t, f, number, core.StateClosed)
	if runs := f.Runs(number); len(runs) != 3 {
		t.Fatalf("the issue had %d runs, want 3", len(runs))
	}
}

// startRefused is a coding backend that never starts an agent.
type startRefused struct{ agent.Backend }

func (startRefused) Start(context.Context, agent.Task, agent.WorkspaceRef, []string) (agent.RunRef, error) {
	return agent.RunRef{}, core.NewError(core.CodeUnavailable, "the agent host answered with password=hunter2")
}

// A run whose agent never starts is exhausted while its issue is still
// Claimed, and may be so before the control zone ever showed its progress.
// The issue is blocked all the same and its progress comment says why.
func TestARunExhaustedBeforeItsAgentStartsBlocksTheClaimedIssue(t *testing.T) {
	t.Parallel()
	inner, err := fake.New(fake.Config{Clock: core.SystemClock{}, Roles: []agent.Role{agent.RoleCoder}})
	if err != nil {
		t.Fatal(err)
	}
	f := harness.New(t, harness.Options{Coder: startRefused{inner}})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.Pass()
	// The dispatcher works through the run before the control zone's next pass.
	for range f.Manifest.Limits.MaxAttempts + 2 {
		f.Tick()
	}
	held := f.Record(number)
	if runs := f.Runs(number); held.State != core.StateClaimed || len(runs) != 1 || runs[0].Status != dispatcher.RunExhausted {
		t.Fatalf("the run is not exhausted in Claimed: %+v %+v", held, runs)
	}
	f.Pass()
	wantSurfaces(t, f, number, core.StateBlocked)
	wantLabels(t, f, number, github.LabelBlocked)
	wantJourneyEnds(t, f, number, []core.State{core.StateClaimed, core.StateBlocked})
	comment := commentContaining(t, f, number, "<!-- sisf:progress ")
	if !strings.Contains(comment, "**Blocked:**") || !strings.Contains(comment, "`UNAVAILABLE`") || strings.Contains(comment, "hunter2") {
		t.Errorf("the progress comment of an issue blocked in Claimed:\n%s", comment)
	}
}

// The dispatcher that exhausts a run, under the agent zone, leaves the run's
// reservation held: that zone makes reservations and changes none. The
// control zone's pass that blocks the issue closes the reservation in the
// same step, so an exhausted run does not count against the budget cap for
// ever.
func TestTheBlockOfAnExhaustedRunClosesTheReservationTheDispatcherLeftHeld(t *testing.T) {
	t.Parallel()
	inner, err := fake.New(fake.Config{Clock: core.SystemClock{}, Roles: []agent.Role{agent.RoleCoder}})
	if err != nil {
		t.Fatal(err)
	}
	f := harness.New(t, harness.Options{Coder: startRefused{inner}})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.Pass()
	for range f.Manifest.Limits.MaxAttempts + 2 {
		f.Tick()
	}
	runs := f.Runs(number)
	if len(runs) != 1 || runs[0].Status != dispatcher.RunExhausted {
		t.Fatalf("the run is not exhausted: %+v", runs)
	}
	reservation := func() core.Reservation {
		t.Helper()
		var r core.Reservation
		err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
			var err error
			r, err = tx.GetReservation(ctx, runs[0].ReservationID)
			return err
		})
		if err != nil {
			t.Fatalf("read the reservation of the exhausted run: %v", err)
		}
		return r
	}
	if r := reservation(); r.Status != core.ReservationHeld {
		t.Fatalf("the dispatcher moved the reservation of the run it exhausted to %s", r.Status)
	}
	f.Pass()
	if got := f.Record(number).State; got != core.StateBlocked {
		t.Fatalf("the issue of the exhausted run is %s, want Blocked", got)
	}
	if r := reservation(); r.Status == core.ReservationHeld || r.Status == core.ReservationSuspended {
		t.Fatalf("the reservation of the blocked run is still %s", r.Status)
	}
}

// The Owner's retry is judged by the record of the run whose block it
// lifts: after the retried issue's next run is released and rolled back,
// the rollback's own record lets the Owner retry it, and the progress of
// the rolled back run does not cite the earlier, exhausted run.
func TestTheRetryOfALaterRollbackIsJudgedByItsOwnRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	failedAttempts(t, f, number)
	f.Cycle(6)
	exhausted := f.Record(number)
	if _, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f)); err != nil {
		t.Fatalf("the Owner's retry: %v", err)
	}
	f.ScriptPatch(number, 0, greetingDiff)
	f.Deploy.FaultNext("app")
	f.Cycle(6)
	rolledBack := f.Record(number)
	if rolledBack.State != core.StateBlocked || rolledBack.RunID == exhausted.RunID || commentContaining(t, f, number, "Release rolled back") == "" {
		t.Fatalf("the second run was not rolled back and blocked: %+v", rolledBack)
	}
	if stop := runStopOf(t, f, rolledBack.RunID); stop.Cause != statemachine.BlockRolledBack || stop.RestoredTo == "" {
		t.Fatalf("the record of the rolled back run: %+v", stop)
	}
	if c := commentContaining(t, f, number, "<!-- sisf:progress "); strings.Contains(c, "coding attempts it was allowed") {
		t.Errorf("the progress of the rolled back run cites the earlier run's stop:\n%s", c)
	}
	maintainer := core.Actor{ID: "user:maintainer", Kind: core.ActorHuman, Roles: []core.Role{core.RoleMaintainer}, Surface: core.SurfaceCLI}
	if _, err := f.Control.Retry(ctx, f.Issue(number), maintainer); !core.HasCode(err, core.CodeGuardFailed) {
		t.Fatalf("a maintainer retried the rolled back release: %v", err)
	}
	if got := f.Record(number); got.State != core.StateBlocked || got.Version != rolledBack.Version {
		t.Fatalf("the refused retry moved the issue: %+v", got)
	}
	if _, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f)); err != nil {
		t.Fatalf("the Owner's retry of the rolled back release: %v", err)
	}
	if got := f.Record(number).State; got != core.StateReady {
		t.Fatalf("the retried issue is %s, want Ready", got)
	}
}
