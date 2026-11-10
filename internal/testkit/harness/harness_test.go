package harness_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

const (
	vagueBody = "The health check is flaky."
	replyBody = "It fails when the database is slow.\n\n### Definition of Done\n- /healthz answers 200 within one second while the database is slow\n"
)

// commentsWith counts the comments on issue number that contain text.
func commentsWith(t *testing.T, f *harness.Factory, number int, text string) int {
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

func wantState(t *testing.T, f *harness.Factory, number int, want core.State) {
	t.Helper()
	if got := f.Record(number).State; got != want {
		t.Fatalf("issue %d is %s, want %s", number, got, want)
	}
	if api, dashboard := f.SurfaceStates(number); api != string(want) || dashboard != string(want) {
		t.Fatalf("the API shows %s and the dashboard %s, want %s", api, dashboard, want)
	}
}

// A request that lacks information is asked about, answered, scoped and
// handed to the agent, and both surfaces show each state.
func TestALabeledIssueGoesFromIntakeToDevelopingThroughAClarification(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Health check is flaky", vagueBody, github.LabelReady, github.LabelKindBug)

	f.Pass()
	wantState(t, f, number, core.StateClarifying)
	if commentsWith(t, f, number, "More information needed") != 1 {
		t.Fatal("no clarifying comment was posted")
	}
	f.Tick()
	wantState(t, f, number, core.StateClarifying) // nothing to claim yet

	f.Comment(number, "maintainer", "MEMBER", replyBody)
	f.Pass()
	wantState(t, f, number, core.StateReady)
	if commentsWith(t, f, number, "Scope of Work v1") != 1 {
		t.Fatal("scope v1 was not attached to the issue")
	}
	issue, err := f.GitHub.Issue(f.Repo, number)
	if err != nil || !hasLabel(issue.Labels, github.LabelApproved) {
		t.Fatalf("the issue does not show it is approved: %+v, %v", issue.Labels, err)
	}

	res := f.Tick()
	if len(res.Claimed) != 1 {
		t.Fatalf("the Ready issue was not claimed: %+v", res)
	}
	// The claim and the agent's start land in one round.
	wantState(t, f, number, core.StateDeveloping)
	journey := f.Journey(number)
	tail := []core.State{core.StateReady, core.StateClaimed, core.StateDeveloping}
	if !slices.Contains(journey, core.StateClarifying) || len(journey) < len(tail) || !slices.Equal(journey[len(journey)-len(tail):], tail) {
		t.Fatalf("the issue went %v", journey)
	}

	runs := f.Runs(number)
	if len(runs) != 1 || !strings.Contains(runs[0].Brief.Handoff.SoW, "database is slow") ||
		runs[0].Brief.Policy.PolicySHA != f.PolicyRevision {
		t.Fatalf("the run does not start from the approved scope under the approved policy: %+v", runs)
	}
	if started := f.Agent.Started(f.Issue(number)); len(started) != 1 {
		t.Fatalf("the agent was started %d times", len(started))
	}
	// The repeated passes posted nothing twice.
	f.Settle(3)
	if commentsWith(t, f, number, "More information needed") != 1 || commentsWith(t, f, number, "Scope of Work v1") != 1 {
		t.Fatal("a comment was posted twice")
	}
}

func hasLabel(labels []string, name string) bool {
	for _, l := range labels {
		if l == name {
			return true
		}
	}
	return false
}

// A dispatcher that is killed and started again resumes its run, and a
// report made under the lease of the earlier incarnation is fenced off.
func TestARestartedDispatcherResumesTheRunAndFencesTheStaleReport(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Health check fails", replyBody, github.LabelReady, github.LabelKindBug)
	issue := f.Issue(number)
	if err := f.Agent.Script(agent.RoleCoder, issue, fake.Hang()); err != nil {
		t.Fatal(err)
	}
	f.Pass()
	f.Tick()
	wantState(t, f, number, core.StateDeveloping)
	run := f.Record(number).RunID
	stale := tokenOf(t, f.Dispatcher, run)

	old := f.RestartDispatcher()
	res := f.Tick()
	if len(res.Resumed) != 1 || len(res.Claimed) != 0 {
		t.Fatalf("the restarted dispatcher did not resume the run: %+v", res)
	}
	if fresh := tokenOf(t, f.Dispatcher, run); fresh <= stale {
		t.Fatalf("the resumed run holds token %d, not newer than %d", fresh, stale)
	}
	report := dispatcher.Report{Issue: issue, RunID: run, LeaseOwner: harness.DispatcherOwner, FencingToken: stale,
		PullRequests: []string{f.Repo + "#99"}}
	for name, d := range map[string]*dispatcher.Dispatcher{"the new dispatcher": f.Dispatcher, "the old one": old} {
		if err := d.Report(ctx, report); !core.HasCode(err, core.CodeStaleFencingToken) {
			t.Fatalf("a stale report through %s was not fenced: %v", name, err)
		}
	}
	if runs := f.Runs(number); len(runs) != 1 || runs[0].RunID != run || len(runs[0].PullRequests) != 0 {
		t.Fatalf("the restart changed the run or the stale report was written: %+v", runs)
	}
	wantState(t, f, number, core.StateDeveloping)
}

func tokenOf(t *testing.T, d *dispatcher.Dispatcher, run core.RunID) core.FencingToken {
	t.Helper()
	for _, info := range d.Health().Runs {
		if info.RunID == run {
			return info.FencingToken
		}
	}
	t.Fatalf("the dispatcher holds no lease for run %s", run)
	return 0
}

// Without the Owner's budget confirmation an issue goes Ready and stays
// there: intake works, nothing is claimed and nothing is spent.
func TestWithoutABudgetConfirmationNothingIsClaimed(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{Unconfirmed: true})
	number := f.OpenIssue("maintainer", "MEMBER", "Health check fails", replyBody, github.LabelReady, github.LabelKindBug)
	f.Pass()
	res := f.Tick()
	if len(res.Claimed) != 0 || !core.HasCode(res.Held, core.CodeBudgetUnconfirmed) {
		t.Fatalf("a claim went through without a budget confirmation: %+v", res)
	}
	wantState(t, f, number, core.StateReady)
	if len(f.Runs(number)) != 0 || len(f.Agent.Started(f.Issue(number))) != 0 {
		t.Fatal("a run was started without a budget confirmation")
	}
}

// Without an approved policy intake does nothing, and the delivery waits in
// the inbox until a policy is in force.
func TestWithoutAnApprovedPolicyIntakeHoldsAndCatchesUpLater(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{NoPolicy: true})
	number := f.OpenIssue("maintainer", "MEMBER", "Health check fails", replyBody, github.LabelReady, github.LabelKindBug)
	if err := f.Control.Pass(context.Background()); !core.HasCode(err, core.CodePolicyHold) {
		t.Fatalf("a pass under no policy did not report the hold: %v", err)
	}
	var detail map[string]any
	if status := f.Get("/api/v1/issues/"+f.Issue(number).String(), &detail); status != 404 {
		t.Fatalf("the issue is tracked under no policy: %d %v", status, detail)
	}
	if _, err := f.Control.ApproveBaseline(context.Background(), f.PolicyRevision, ""); err != nil {
		t.Fatal(err)
	}
	f.Clock.Advance(time.Minute)
	f.Pass()
	wantState(t, f, number, core.StateReady)
}

// After the Owner rejects a new feature, the Owner's retry asks for the
// approval again, and the approval of that request lets the retry through.
func TestTheOwnersRetryAsksAgainAndThenGoesThrough(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	body := "Add a page that lists recent deploys.\n\n### Definition of Done\n- /deploys lists the last ten deploys\n"
	number := f.OpenIssue("maintainer", "MEMBER", "Deploy history page", body, github.LabelReady, github.LabelKindFeature)
	issue := f.Issue(number)
	f.Pass()
	wantState(t, f, number, core.StateApprovalPending)
	pending, err := f.Control.Approvals.List(ctx, approvals.Filter{IssueID: issue, Statuses: []approvals.Status{approvals.StatusPending}})
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending requests = %+v, %v", pending, err)
	}
	decide(t, f, pending[0], approvals.DecisionReject)
	f.Pass()
	wantState(t, f, number, core.StateBlocked)
	f.Clock.Advance(time.Hour)

	owner := ownerOf(f)
	_, err = f.Control.Retry(ctx, issue, owner)
	if !core.HasCode(err, core.CodeApprovalRequired) {
		t.Fatalf("the first retry did not ask for an approval: %v", err)
	}
	again, err := f.Control.Approvals.List(ctx, approvals.Filter{IssueID: issue, Statuses: []approvals.Status{approvals.StatusPending}})
	if err != nil || len(again) != 1 || again[0].ID == pending[0].ID {
		t.Fatalf("no new request was filed: %+v, %v", again, err)
	}
	// Someone who is not the Owner cannot retry, approved or not.
	decide(t, f, again[0], approvals.DecisionApprove)
	maintainer := core.Actor{ID: "user:maintainer", Kind: core.ActorHuman, Roles: []core.Role{core.RoleMaintainer}, Surface: core.SurfaceCLI}
	if _, err := f.Control.Retry(ctx, issue, maintainer); !core.HasCode(err, core.CodeGuardFailed) {
		t.Fatalf("a maintainer's retry was not refused: %v", err)
	}
	if _, err := f.Control.Retry(ctx, issue, owner); err != nil {
		t.Fatalf("the retry on the new approval was refused: %v", err)
	}
	f.Pass()
	wantState(t, f, number, core.StateReady)
}

func ownerOf(f *harness.Factory) core.Actor {
	return core.Actor{ID: "owner:local", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner}, Surface: core.SurfaceCLI, StepUpAt: f.Clock.Now()}
}

func decide(t *testing.T, f *harness.Factory, r approvals.Request, decision approvals.Decision) {
	t.Helper()
	_, err := f.Control.Approvals.Submit(context.Background(), ownerOf(f), approvals.Submission{RequestID: r.ID, Nonce: r.Nonce,
		SubjectDigest: r.SubjectDigest, PolicyRevision: r.PolicyRevision, Decision: decision})
	if err != nil {
		t.Fatalf("the Owner's decision was not recorded: %v", err)
	}
}
