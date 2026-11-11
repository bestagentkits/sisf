//go:build unix

package acceptance

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

const greetedMain = "package main\n\nfunc main() { println(\"hello, world\") }\n"

// greetingIssue opens the greeting issue with its patch scripted.
func greetingIssue(t *testing.T) (*harness.Factory, int) {
	t.Helper()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	return f, number
}

// candidatePR reads the supervisor's record of the run's pull request.
func candidatePR(t *testing.T, f *harness.Factory, number int) handoff.CandidatePR {
	t.Helper()
	var pr handoff.CandidatePR
	err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		pr, err = handoff.ReadCandidatePR(ctx, tx, f.Record(number).RunID)
		return err
	})
	if err != nil {
		t.Fatalf("the run of issue %d has no pull request: %v", number, err)
	}
	return pr
}

// mainHolds reports whether the default branch carries the greeting.
func mainHolds(f *harness.Factory) bool {
	_, files, ok := f.GitData.Branch(f.Repo, "main")
	return ok && files["examples/hello-app/main.go"] == greetedMain
}

// The candidate QA passed gets one pull request from its branch, linked to
// the issue without closing it. Once its release is verified the
// supervisor merges it at the candidate commit, the progress comment says
// so, and only then is the issue closed.
func TestAVerifiedCandidateIsMergedByTheSupervisorBeforeTheIssueCloses(t *testing.T) {
	t.Parallel()
	f, number := greetingIssue(t)
	f.Cycle(3)
	wantSurfaces(t, f, number, core.StateClosed)

	pr := candidatePR(t, f, number)
	if pr.State != handoff.LandingMerged || pr.Branch != "sisf/"+string(f.Manifest.FactoryID)+"/"+string(f.Record(number).RunID)+"-1" || pr.Base != "main" {
		t.Fatalf("the candidate's pull request: %+v", pr)
	}
	view, err := f.GitHub.Issue(f.Repo, pr.Number)
	if err != nil || !strings.Contains(view.Body, "Refs #") || strings.Contains(strings.ToLower(view.Body), "closes #") ||
		!strings.Contains(view.Title, "Greet the world") || !strings.Contains(view.Body, "QA receipt") || !strings.Contains(view.Body, "Scope of Work") {
		t.Fatalf("the pull request reads %+v, %v", view, err)
	}
	if !mainHolds(f) || f.GitData.Merges() != 1 {
		t.Fatalf("main does not carry the candidate after %d merges", f.GitData.Merges())
	}
	progress := commentContaining(t, f, number, "<!-- sisf:progress ")
	for _, want := range []string{"QA attempt 1 of 3: passed", "Candidate pull request: #", "**Merged** #", "Closed:", "the change is merged"} {
		if !strings.Contains(progress, want) {
			t.Errorf("the progress comment lacks %q:\n%s", want, progress)
		}
	}
	if n := countComments(t, f, number, "<!-- sisf:progress "); n != 1 {
		t.Errorf("the issue carries %d progress comments", n)
	}
	if seen := f.Inspect(number); seen.PullRequest == nil || seen.PullRequest.Number != pr.Number || seen.PullRequest.State != string(handoff.LandingMerged) {
		t.Errorf("runs.inspect shows the pull request %+v", seen.PullRequest)
	}
	// QA's pass closed the run's budget reservation.
	if _, reservation := ledgerOf(t, f, f.Runs(number)[0].ReservationID); reservation.Status == core.ReservationHeld {
		t.Errorf("the reservation of a run QA passed is still %s", reservation.Status)
	}
}

// When GitHub refuses the merge because the branch moved, the change is
// left for the Owner: the issue still closes on its verified release, and
// the closing checkpoint says the change is not merged.
func TestACandidateWhoseBranchMovedIsLeftForTheOwner(t *testing.T) {
	t.Parallel()
	f, number := greetingIssue(t)
	f.GitData.FailOn(http.MethodPut, "/merge", http.StatusConflict, "Head branch was modified. Review and try the merge again.")
	f.Cycle(3)
	wantSurfaces(t, f, number, core.StateClosed)
	pr := candidatePR(t, f, number)
	if pr.State != handoff.LandingLeftOpen || !strings.Contains(pr.Reason, "branch moved") || mainHolds(f) {
		t.Fatalf("a candidate whose branch moved: %+v; main holds it: %t", pr, mainHolds(f))
	}
	progress := commentContaining(t, f, number, "<!-- sisf:progress ")
	if !strings.Contains(progress, "**Not merged:**") || !strings.Contains(progress, "the change is **not** merged") {
		t.Errorf("the progress comment does not say the change is not merged:\n%s", progress)
	}
	if note := commentContaining(t, f, pr.Number, "<!-- sisf:landing "); !strings.Contains(note, "left open for the Owner") {
		t.Errorf("the pull request carries no note: %q", note)
	}
	if why := f.Explain(number); !hasEvidence(why, "candidate_pr") || !strings.Contains(why.Reason, "not merged") {
		t.Errorf("runs.explain answers %+v", why)
	}
}

// A release that is rolled back is never merged: its pull request stays
// open with a note, and the progress comment shows the rollback.
func TestTheCandidateOfARolledBackReleaseIsNeverMerged(t *testing.T) {
	t.Parallel()
	f, number := greetingIssue(t)
	f.Deploy.FaultNext("app")
	f.Cycle(4)
	wantSurfaces(t, f, number, core.StateBlocked)
	pr := candidatePR(t, f, number)
	if pr.State != handoff.LandingRolledBack || f.GitData.Merges() != 0 || mainHolds(f) {
		t.Fatalf("the rolled-back candidate: %+v after %d merges", pr, f.GitData.Merges())
	}
	if view, err := f.GitHub.Issue(f.Repo, pr.Number); err != nil || view.State != "open" {
		t.Fatalf("the pull request is %q, %v", view.State, err)
	}
	if note := commentContaining(t, f, pr.Number, "<!-- sisf:landing "); !strings.Contains(note, "rolled back") {
		t.Errorf("the pull request carries no rollback note: %q", note)
	}
	if progress := commentContaining(t, f, number, "<!-- sisf:progress "); !strings.Contains(progress, "**Rolled back**") {
		t.Errorf("the progress comment does not show the rollback:\n%s", progress)
	}
}

// GitHub out of service as the supervisor merges: the issue waits in
// Verified, the merge is tried again in a later round (as after a restart),
// the pull request is merged exactly once and only then is the issue
// closed.
func TestAMergeInterruptedAfterVerificationIsFinishedExactlyOnce(t *testing.T) {
	t.Parallel()
	f, number := greetingIssue(t)
	for range 4 { // every try of the client's request
		f.GitData.FailOn(http.MethodPut, "/merge", http.StatusBadGateway, "Server Error")
	}
	for range 6 {
		if f.Cycle(1); f.Record(number).State == core.StateVerified {
			break
		}
	}
	if r := f.Record(number); r.State != core.StateVerified || candidatePR(t, f, number).State != handoff.LandingPending {
		t.Fatalf("after the outage the issue is %s with its pull request %+v", r.State, candidatePR(t, f, number))
	}
	if gh, _ := f.GitHub.Issue(f.Repo, number); gh.State != "open" {
		t.Fatalf("the issue closed on GitHub before its change was merged")
	}
	f.Cycle(2)
	wantSurfaces(t, f, number, core.StateClosed)
	if pr := candidatePR(t, f, number); pr.State != handoff.LandingMerged || f.GitData.Merges() != 1 || !mainHolds(f) {
		t.Fatalf("after the restart: %+v, %d merges", pr, f.GitData.Merges())
	}
}
