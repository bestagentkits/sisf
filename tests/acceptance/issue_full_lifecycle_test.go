package acceptance

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/factoryd/lifecycle"
	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// An issue goes the whole way: scoped, claimed, developed, built into a
// candidate, judged by an independent evaluator whose receipt the
// supervisor signs, released by canary, observed, verified, published and
// closed. GitHub and the dashboard agree on where it is at every stop.
func TestAnIssueIsDevelopedEvaluatedReleasedVerifiedAndClosed(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	issue := f.Issue(number)
	f.ScriptPatch(number, 0, greetingDiff)

	f.Pass()
	wantSurfaces(t, f, number, core.StateReady)
	f.Tick()
	f.Pass()
	wantSurfaces(t, f, number, core.StateDeveloping)
	wantLabels(t, f, number, github.LabelRunning)
	f.Cycle(3)
	wantSurfaces(t, f, number, core.StateClosed)
	wantJourneyEnds(t, f, number, fullLifecycle)

	// QA judged it as an evaluator distinct from the coder, on its own route.
	judged := f.Agent.Judged(issue)
	if len(judged) != 1 || judged[0].Identity == lifecycle.CoderIdentity("fake") ||
		judged[0].ModelRoute == judged[0].Candidate.CodingModelRoute {
		t.Fatalf("the evaluation was not independent: %+v", judged)
	}
	// The candidate lives on this factory's own branch, built from the patch.
	record := f.Record(number)
	branch := stages.Branch(f.Manifest.FactoryID, record.RunID, 1)
	_, files, ok := f.GitData.Branch(f.Repo, branch)
	if !ok || files["app/main.go"] != "package main\n\nfunc main() { println(\"hello, world\") }\n" {
		t.Fatalf("the candidate branch %s holds %v", branch, files["app/main.go"])
	}
	// The release went 5, 25 and 100 percent and the app is the new build.
	staged := f.Deploy.Staged()
	if len(staged) != 1 || staged[0].Unit != "app" || core.Digest(staged[0].Digest) != record.CandidateDigest {
		t.Fatalf("staged %+v for candidate %s", staged, record.CandidateDigest)
	}
	if lkg, found, err := f.Trust.LKG.Get(context.Background(), "app"); err != nil || !found || core.Digest(lkg.Digest) != record.CandidateDigest {
		t.Fatalf("the verified release is not the last known good: %+v, %v", lkg, err)
	}
	if sent, failed := f.Probed("app"); sent == 0 || failed != 0 {
		t.Fatalf("the app served %d requests with %d server errors during the release", sent, failed)
	}
	// GitHub shows the issue closed, and the Publisher recorded the release.
	gh, err := f.GitHub.Issue(f.Repo, number)
	if err != nil || gh.State != "closed" {
		t.Fatalf("GitHub shows the issue %q: %v", gh.State, err)
	}
	wantLabels(t, f, number, github.LabelVerified)
	if !slices.ContainsFunc(f.GitData.Branches(f.Repo), func(b string) bool { return b == "sisf/release/"+string(record.RunID) }) {
		t.Fatalf("the Publisher wrote no changelog branch: %v", f.GitData.Branches(f.Repo))
	}
}

// An issue that arrives without its definition of done is asked for it on
// GitHub, the author's answer makes Scope of Work v1 with a digest, and the
// coding agent works from exactly that version until the issue is closed.
// GitHub and the dashboard agree on where it is at every stop.
func TestAnIncompleteIssueIsClarifiedScopedAndThenRunToClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", "The greeting is too terse.",
		github.LabelReady, github.LabelKindBug)

	f.Pass()
	wantSurfaces(t, f, number, core.StateClarifying)
	question := commentContaining(t, f, number, "More information needed")
	if question == "" || !strings.Contains(question, "Definition of Done") {
		t.Fatalf("the clarification comment does not ask for the definition of done: %q", question)
	}
	if _, err := f.Control.Intake.ApprovedScope(ctx, f.Issue(number)); !core.HasCode(err, core.CodeApprovalRequired) {
		t.Fatalf("an unanswered issue has a scope: %v", err)
	}
	// Nothing is claimed while the question is open.
	f.Tick()
	if runs := f.Runs(number); len(runs) != 0 {
		t.Fatalf("an issue being clarified was claimed: %+v", runs)
	}

	f.Comment(number, "maintainer", "MEMBER", "### Definition of Done\n- the app greets the world\n")
	f.Pass()
	wantSurfaces(t, f, number, core.StateReady)
	scope, err := f.Control.Intake.ApprovedScope(ctx, f.Issue(number))
	if err != nil || scope.Version != 1 || scope.Digest.Validate() != nil || scope.PolicyRevision != f.PolicyRevision ||
		!slices.Equal(scope.DoD, []string{"the app greets the world"}) {
		t.Fatalf("the answer did not record Scope of Work v1: %+v, %v", scope, err)
	}
	sow := commentContaining(t, f, number, "### Scope of Work v1")
	if sow == "" || !strings.Contains(sow, string(scope.Digest)) {
		t.Fatalf("the versioned scope is not on the issue: %q", sow)
	}

	f.ScriptPatch(number, 0, greetingDiff)
	f.Tick()
	f.Pass()
	wantSurfaces(t, f, number, core.StateDeveloping)
	runs := f.Runs(number)
	if len(runs) != 1 || runs[0].Brief.ScopeDigest != scope.Digest {
		t.Fatalf("the run was not briefed from Scope of Work v1 (%s): %+v", scope.Digest, runs)
	}
	f.Cycle(3)
	wantSurfaces(t, f, number, core.StateClosed)
	if journey, want := f.Journey(number), append([]core.State{core.StateIntake, core.StateClarifying, core.StateScopeDraft},
		fullLifecycle...); !slices.Equal(journey, want) {
		t.Fatalf("issue %d went %v, want %v", number, journey, want)
	}
	if gh, err := f.GitHub.Issue(f.Repo, number); err != nil || gh.State != "closed" {
		t.Fatalf("GitHub shows the issue %q: %v", gh.State, err)
	}
	wantLabels(t, f, number, github.LabelVerified)
}

// A candidate the evaluator fails goes back to its coding agent with the
// next attempt number, and the second candidate is released.
func TestAFailedEvaluationRetriesWithTheNextAttemptAndThenReleases(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	f.ScriptPatch(number, 0, greetingDiff+"")
	f.ScriptQA(number, map[qa.Category]int{qa.CategoryCorrectness: 1, qa.CategorySecurity: 1, qa.CategoryContract: 1,
		qa.CategoryExperience: 1, qa.CategoryObservability: 1})

	f.Cycle(6)
	wantSurfaces(t, f, number, core.StateClosed)
	journey := f.Journey(number)
	if i := slices.Index(journey, core.StateQAEvaluation); i < 0 || journey[i+1] != core.StateDeveloping {
		t.Fatalf("the failed evaluation did not send the issue back to development: %v", journey)
	}
	if record := f.Record(number); record.Attempt != 2 {
		t.Fatalf("the issue closed at attempt %d, want 2", record.Attempt)
	}
	runs := f.Runs(number)
	if len(runs) != 1 || runs[0].Attempt != 2 || len(runs[0].Brief.Handoff.Decisions) == 0 {
		t.Fatalf("the run did not restart its coding agent with the evaluator's verdict: %+v", runs)
	}
}

// A canary that fails its users is rolled back to the last known good
// release, and the issue is Blocked with a recovery report on it.
func TestAFailingCanaryIsRolledBackAndTheIssueBlocked(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	f.Deploy.FaultNext("app")
	f.Cycle(4)
	wantSurfaces(t, f, number, core.StateBlocked)
	wantLabels(t, f, number, github.LabelBlocked)
	if observed, err := f.Deploy.ObservedRevision(context.Background(), "app"); err != nil || observed.Digest != f.Trust.Installed["app"] {
		t.Fatalf("after the rollback the app runs %+v, %v", observed, err)
	}
	if commentContaining(t, f, number, "Release rolled back") == "" {
		t.Fatal("no recovery report was posted on the issue")
	}
}

// With no real traffic to judge by and no synthetic suite, a release holds
// before anything is deployed: missing metrics are never success.
func TestAReleaseWithoutMetricsHolds(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	f.Quiet()
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	f.Cycle(4)
	wantSurfaces(t, f, number, core.StateReleaseCandidate)
	if staged := f.Deploy.Staged(); len(staged) != 0 {
		t.Fatalf("a release without metrics was deployed: %+v", staged)
	}
}

// A patch that does not apply to the approved commit is never released:
// it fails evaluation without being judged, and the run's next coding
// attempt is built and released.
func TestAPatchThatCannotBeBuiltFailsEvaluationAndIsRetried(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, strings.Replace(greetingDiff, `println("hello")`, `println("goodbye")`, 1))
	f.ScriptPatch(number, 0, greetingDiff)

	f.Cycle(6)
	wantSurfaces(t, f, number, core.StateClosed)
	journey := f.Journey(number)
	if i := slices.Index(journey, core.StateQAEvaluation); i < 0 || journey[i+1] != core.StateDeveloping {
		t.Fatalf("the unbuildable patch was not sent back to development: %v", journey)
	}
	if judged := f.Agent.Judged(f.Issue(number)); len(judged) != 1 {
		t.Fatalf("the unbuildable patch was judged: %d evaluations", len(judged))
	}
	if record := f.Record(number); record.Attempt != 2 {
		t.Fatalf("the issue closed at attempt %d, want 2", record.Attempt)
	}
}
