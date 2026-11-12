//go:build unix

package acceptance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/actions/runactions"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// When the last attempt a run is allowed fails QA, the verdict is held and
// the issue stays in QAEvaluation, which the lifecycle has no step out of.
// The factory says so where the Owner looks: the issue's one progress
// comment, edited at every attempt, names the attempt, the score against
// the pass mark and the failed gates; runs.explain reports the run
// stopped. A candidate QA did not pass gets no pull request. The run's reservation is
// closed, and what its real agents reported is in the ledger: the Claude
// coder's cost for every attempt and the Codex judge's tokens.
func TestAQAHoldAtTheAttemptLimitIsShownToTheOwnerAndTheRunIsBooked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := agentFactory(t, []clitest.Script{{Steps: qaRun(0)}, {Steps: qaRun(0)}, {Steps: qaRun(0)}})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	attempts := f.Manifest.Limits.MaxAttempts

	held := false
	for range 30 {
		f.Cycle(1)
		if r := f.Record(number); r.Attempt == attempts && r.State == core.StateQAEvaluation && hasQAResult(t, f, r.RunID, attempts) {
			held = true
			break
		}
	}
	if !held {
		t.Fatalf("issue %d is %s at attempt %d; journey %v", number, f.Record(number).State, f.Record(number).Attempt, f.Journey(number))
	}
	if r := qaResult(t, f, number, attempts); r.Verdict != stages.VerdictHeld || r.Receipt.Score != 70 {
		t.Fatalf("the last evaluation: verdict %s, score %d, reason %q", r.Verdict, r.Receipt.Score, r.Reason)
	}
	f.Cycle(1) // the control zone posts what the supervisor queued

	// The issue's one progress comment, edited in place through the three
	// attempts, ends with why the run stopped.
	comment := commentContaining(t, f, number, "<!-- sisf:progress ")
	for _, want := range []string{"QA attempt 1 of 3: failed", "QA attempt 2 of 3: failed", "QA attempt 3 of 3: held",
		"attempt 3 of 3", "score 70", "pass mark 85", "QAEvaluation"} {
		if !strings.Contains(comment, want) {
			t.Errorf("the progress comment lacks %q:\n%s", want, comment)
		}
	}
	if strings.Contains(comment, "Candidate pull request") {
		t.Errorf("a candidate QA held got a pull request:\n%s", comment)
	}
	f.Cycle(2)
	if n := countComments(t, f, number, "<!-- sisf:progress "); n != 1 {
		t.Errorf("the issue carries %d progress comments", n)
	}
	if n := countComments(t, f, number, "<!-- sisf:qa-held "); n != 0 {
		t.Errorf("the hold was posted as a comment of its own %d times", n)
	}

	// runs.explain reports the run stopped, with the hold as evidence.
	why := f.Explain(number)
	if !why.Stopped || why.Cause != runactions.CauseAttemptsExhausted || !strings.Contains(why.Reason, "score 70") || !hasEvidence(why, "qa_hold") {
		t.Errorf("runs.explain answers %+v", why)
	}
	// runs.inspect carries the hold beside the dispatcher's finished run.
	if seen := f.Inspect(number); seen.QAHold == nil || !strings.Contains(seen.QAHold.Summary, "attempt 3 of 3") {
		t.Errorf("runs.inspect shows the hold %+v", seen.QAHold)
	}

	// The Owner's retry finds no step out of a held evaluation.
	owner := core.Actor{ID: "owner:local", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner}, Surface: core.SurfaceCLI, StepUpAt: f.Clock.Now()}
	if _, err := f.Control.Retry(ctx, f.Issue(number), owner); err == nil {
		t.Errorf("the Owner's retry of a held evaluation was taken")
	}
	if r := f.Record(number); r.State != core.StateQAEvaluation {
		t.Errorf("the retry moved the held issue to %s", r.State)
	}

	// The run is booked and its reservation closed.
	run := f.Runs(number)[0]
	totals, reservation := ledgerOf(t, f, run.ReservationID)
	if reservation.Status == core.ReservationHeld || totals.Reserved != 0 {
		t.Errorf("the run's reservation is still %s; %d reserved", reservation.Status, totals.Reserved)
	}
	if want := core.Micros(attempts * 10_000); totals.SpentUnverified < want || run.Spend.ReportedCost != want {
		t.Errorf("the ledger shows %+v and the run reported %+v, want the coder's %d for %d attempts", totals, run.Spend, want, attempts)
	}
}

// hasQAResult reports whether the supervisor recorded a verdict for attempt.
func hasQAResult(t *testing.T, f *harness.Factory, run core.RunID, attempt int) bool {
	t.Helper()
	err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		_, err := stages.ReadQAResult(ctx, tx, run, attempt)
		return err
	})
	return err == nil
}

// countComments counts the comments on issue number holding text.
func countComments(t *testing.T, f *harness.Factory, number int, text string) int {
	t.Helper()
	comments, err := f.GitHub.Comments(f.Repo, number)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range comments {
		if strings.Contains(c.Body, text) {
			n++
		}
	}
	return n
}

func hasEvidence(e runactions.Explanation, kind string) bool {
	for _, ev := range e.Evidence {
		if ev.Kind == kind {
			return true
		}
	}
	return false
}

// ledgerOf reads the ledger's totals and one reservation.
func ledgerOf(t *testing.T, f *harness.Factory, reservationID string) (core.BudgetTotals, core.Reservation) {
	t.Helper()
	var totals core.BudgetTotals
	var reservation core.Reservation
	err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		if totals, err = tx.BudgetTotals(ctx, time.Time{}); err != nil {
			return err
		}
		reservation, err = tx.GetReservation(ctx, reservationID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return totals, reservation
}
