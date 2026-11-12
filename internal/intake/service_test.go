package intake_test

import (
	"context"
	"sync"
	"testing"

	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

func TestIntakeIsNotBuiltWithoutItsParts(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	whole := intake.Config{State: f.state, Policy: f.policy, Issues: f.github, Approvals: f.approvals}
	if _, err := intake.New(whole); err != nil {
		t.Fatalf("a complete configuration was refused: %v", err)
	}
	broken := map[string]func(*intake.Config){
		"no state store":      func(c *intake.Config) { c.State = nil },
		"no policy source":    func(c *intake.Config) { c.Policy = nil },
		"no issue reader":     func(c *intake.Config) { c.Issues = nil },
		"no approval store":   func(c *intake.Config) { c.Approvals = nil },
		"a negative lifetime": func(c *intake.Config) { c.ApprovalTTL = -1 },
	}
	for name, breakIt := range broken {
		cfg := whole
		breakIt(&cfg)
		_, err := intake.New(cfg)
		wantCode(t, err, core.CodeInvalidArgument, name)
	}
}

func TestWithoutAnApprovedPolicyIntakeDoesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	hold := core.NewError(core.CodePolicyHold, "the policy revision is not approved")

	// A new issue is not taken in, and GitHub is not even asked about it.
	f := newFactory(t)
	f.open(member, completeBody, github.LabelReady, github.LabelKindBug)
	f.policy.set(nil, hold)
	_, err := f.service.Reconcile(ctx, issueRef)
	wantCode(t, err, core.CodePolicyHold, "intake under a policy hold")
	if f.stateOf() != "" || f.github.readCount() != 0 || len(f.state.Outbox()) != 0 {
		t.Fatal("intake acted without an approved policy")
	}

	// An issue with an approval waiting does not become Ready during a hold.
	g, request := proposed(t)
	g.decide(request, approvals.DecisionApprove)
	g.policy.set(nil, hold)
	_, err = g.service.Reconcile(ctx, issueRef)
	wantCode(t, err, core.CodePolicyHold, "using an approval under a policy hold")
	g.wantState(core.StateApprovalPending)
	if got := g.requestFor(request.SubjectDigest); got.Status != approvals.StatusApproved {
		t.Fatalf("the approval was spent during a hold: %s", got.Status)
	}

	// A policy without a manifest is no policy to act under.
	h := newFactory(t)
	h.open(member, completeBody, github.LabelReady, github.LabelKindBug)
	h.policy.set(func(s *policy.Snapshot) { s.Manifest = nil }, nil)
	_, err = h.service.Reconcile(ctx, issueRef)
	wantCode(t, err, core.CodePolicyHold, "a policy without a manifest")
	if h.stateOf() != "" {
		t.Fatal("intake acted under a policy without a manifest")
	}
}

func TestIssuesThatAreNotForThisFactoryAreLeftAlone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFactory(t)
	f.open(member, completeBody, github.LabelReady, github.LabelKindBug)

	// Another repository than the one the approved manifest names.
	foreign := core.IssueRef{Owner: "someone-else", Repo: "hello-app", Number: 7}
	result, err := f.service.Reconcile(ctx, foreign)
	if err != nil || result.Skipped == "" || f.github.readCount() != 0 {
		t.Fatalf("a foreign issue was looked at: %+v, %v", result, err)
	}
	_, err = f.service.Reconcile(ctx, core.IssueRef{Owner: "acme", Repo: "hello-app"})
	wantCode(t, err, core.CodeInvalidArgument, "an issue without a number")

	// A closed issue is not work, whether new or half-way through intake.
	f.github.edit(issueRef, func(issue *github.Issue) { issue.State = github.IssueClosed })
	if result := f.reconcile(); result.Skipped == "" || f.stateOf() != "" {
		t.Fatalf("a closed issue was taken in: %+v", result)
	}
	g, request := proposed(t)
	g.decide(request, approvals.DecisionApprove)
	g.github.edit(issueRef, func(issue *github.Issue) { issue.State = github.IssueClosed })
	if result := g.reconcile(); result.Skipped == "" || len(result.Events) != 0 {
		t.Fatalf("a closed issue moved on: %+v", result)
	}
	g.wantState(core.StateApprovalPending)
}

func TestEventsOnlyPointAtAnIssue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFactory(t)
	f.open(member, completeBody, github.LabelReady, github.LabelKindBug)

	skipped := map[string]github.WebhookEvent{
		"the factory's own edit": f.event("issues", "labeled", func(ev *github.WebhookEvent) { ev.FromSelf = true }),
		"a push":                 f.event("push", "", func(ev *github.WebhookEvent) { ev.Issue = nil }),
		"a pull request": f.event("pull_request", "opened", func(ev *github.WebhookEvent) {
			ev.Issue, ev.PullRequest = nil, &github.PullRequest{}
		}),
		"an issue event without an issue": f.event("issues", "opened", func(ev *github.WebhookEvent) { ev.Issue = nil }),
		// The payload names an issue of another repository than the one the
		// delivery was verified for.
		"an issue of another repository": f.event("issues", "opened", func(ev *github.WebhookEvent) {
			ev.Repo = github.RepoRef{Owner: "someone-else", Name: "hello-app"}
		}),
	}
	reads := f.github.readCount()
	for name, ev := range skipped {
		result, err := f.service.HandleEvent(ctx, ev)
		if err != nil || result.Skipped == "" || len(result.Events) != 0 {
			t.Errorf("%s: %+v, %v", name, result, err)
		}
	}
	if f.stateOf() != "" || f.github.readCount() != reads {
		t.Fatal("a skipped event caused work")
	}
	_, err := f.service.HandleEvent(ctx, f.event("issues", "opened", func(ev *github.WebhookEvent) { ev.DeliveryID = "" }))
	wantCode(t, err, core.CodeInvalidArgument, "an event without a delivery id")

	// What the event carries is not trusted: the payload below claims a
	// complete, approved bug by a maintainer, while the issue on GitHub is
	// an outsider's. Intake acts on what GitHub shows.
	f.open(outsider, completeBody, github.LabelReady, github.LabelKindBug)
	forged := f.event("issues", "labeled", func(ev *github.WebhookEvent) {
		claimed := *ev.Issue
		claimed.Author, claimed.AuthorAssociation = member.user, member.association
		claimed.Labels = append(claimed.Labels, github.Label{Name: github.LabelApproved})
		ev.Issue = &claimed
	})
	f.handle(forged)
	f.wantState(core.StateApprovalPending)

	// A comment event leads to the same issue.
	g := newFactory(t)
	g.open(member, completeBody, github.LabelReady, github.LabelKindBug)
	comment := g.github.comment(issueRef, member, "Please look at this.")
	g.handle(g.event("issue_comment", "created", func(ev *github.WebhookEvent) { ev.Issue, ev.Comment = nil, &comment }))
	g.wantState(core.StateReady)
}

func TestDeliveriesThatRepeatOrArriveOutOfOrderChangeNothing(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	f.open(member, featureBody, github.LabelReady, github.LabelKindFeature)
	opened := f.event("issues", "opened", nil)
	labeled := f.event("issues", "labeled", func(ev *github.WebhookEvent) { ev.Label = &github.Label{Name: github.LabelReady} })
	wantEvents(t, f.handle(labeled), core.EventIssueReceived, core.EventInfoSufficient, core.EventApprovalRequired)
	for _, ev := range []github.WebhookEvent{opened, labeled, labeled, opened} {
		wantEvents(t, f.handle(ev))
	}
	if len(f.inbox()) != 1 || f.count("### Scope of Work v1") != 1 || f.count("### Waiting for approval") != 1 {
		t.Fatalf("repeated deliveries repeated work: %d requests, comments %q", len(f.inbox()), f.github.factoryComments(issueRef))
	}
}

func TestConcurrentRunsTakeEachStepOnce(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	f.open(member, featureBody, github.LabelReady, github.LabelKindFeature)
	const runs = 8
	var wg sync.WaitGroup
	errs := make([]error, runs)
	for i := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.service.Reconcile(context.Background(), issueRef)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		// A run that keeps losing the race reports it; it never forces a step.
		if err != nil && !core.HasCode(err, core.CodeConflict) {
			t.Fatalf("a concurrent run failed: %v", err)
		}
	}
	f.reconcile()
	f.wantState(core.StateApprovalPending)
	if len(f.inbox()) != 1 || f.count("### Scope of Work v1") != 1 || f.count("### Waiting for approval") != 1 {
		t.Fatalf("concurrent runs repeated work: %d requests, comments %q", len(f.inbox()), f.github.factoryComments(issueRef))
	}
}

func TestALostWriteIsRetriedFromWhatIsStoredNow(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	f.open(member, completeBody, github.LabelReady, github.LabelKindBug)
	f.state.ConflictNextWrites(2)
	f.reconcile()
	f.wantState(core.StateReady)
	if f.count("### Scope of Work v1") != 1 {
		t.Fatalf("the retried step repeated its comment: %q", f.github.factoryComments(issueRef))
	}

	// A writer that keeps losing gives up with the conflict.
	g := newFactory(t)
	g.open(member, completeBody, github.LabelReady, github.LabelKindBug)
	g.state.ConflictNextWrites(100)
	_, err := g.service.Reconcile(context.Background(), issueRef)
	wantCode(t, err, core.CodeConflict, "a persistent conflict")
	if g.stateOf() == core.StateReady {
		t.Fatal("the issue became Ready through writes that all lost")
	}
}

func TestAGitHubOutageStopsIntakeWithoutLosingAnything(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFactory(t)
	f.open(member, completeBody, github.LabelReady, github.LabelKindBug)
	f.github.mu.Lock()
	f.github.down = true
	f.github.mu.Unlock()
	_, err := f.service.Reconcile(ctx, issueRef)
	wantCode(t, err, core.CodeUnavailable, "intake while GitHub is down")
	if f.stateOf() != "" {
		t.Fatal("intake took a step it could not see the issue for")
	}
	f.github.mu.Lock()
	f.github.down = false
	f.github.mu.Unlock()
	f.reconcile()
	f.wantState(core.StateReady)
}
