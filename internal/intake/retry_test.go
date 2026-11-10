package intake_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// allowed is the answer of the authorization layer that lets an actor retry.
var allowed = core.AuthzDecision{Effect: core.EffectAllow}

// retryBy is the request of an actor who was authorized to retry the issue.
func (f *factory) retryBy(actor core.Actor) intake.RetryRequest {
	return intake.RetryRequest{Issue: issueRef, Actor: actor, Decision: allowed}
}

// refused opens a new-feature request, has the Owner reject it and runs
// intake, which blocks the issue. It returns the rejected request. The clock
// then moves on, so whatever follows happens after the block.
func refused(t *testing.T) (*factory, approvals.Request) {
	t.Helper()
	f, request := proposed(t)
	f.decide(request, approvals.DecisionReject)
	f.reconcile()
	f.wantState(core.StateBlocked)
	f.clock.Advance(time.Hour)
	return f, request
}

// askAgain files a new request to approve what r asked for, as the surface
// through which the Owner is asked a second time does. change may alter it.
func (f *factory) askAgain(r approvals.Request, id string, change func(*approvals.Request)) approvals.Request {
	f.t.Helper()
	nonce, err := approvals.NewNonce()
	if err != nil {
		f.t.Fatal(err)
	}
	again := r
	again.ID, again.Nonce, again.Status = id, nonce, approvals.StatusPending
	again.CreatedAt, again.ExpiresAt = f.clock.Now(), f.clock.Now().Add(intake.DefaultApprovalTTL)
	if change != nil {
		change(&again)
	}
	created, err := f.approvals.Create(context.Background(), again)
	if err != nil {
		f.t.Fatalf("the new approval request could not be filed: %v", err)
	}
	return created
}

// statusOf returns where the approval request with id stands.
func (f *factory) statusOf(id string) approvals.Status {
	f.t.Helper()
	r, err := f.approvals.Get(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return r.Status
}

// wantStillBlocked fails the test unless the refused retry left everything
// as it was: the issue blocked, no scope handed out, nothing queued.
func (f *factory) wantStillBlocked(name string, queued int) {
	f.t.Helper()
	f.wantState(core.StateBlocked)
	if _, err := f.service.ApprovedScope(context.Background(), issueRef); !core.HasCode(err, core.CodeApprovalRequired) {
		f.t.Fatalf("%s: a scope was handed out: %v", name, err)
	}
	if _, err := f.service.ApprovalDenial(context.Background(), issueRef); err != nil {
		f.t.Fatalf("%s: the record of the denial is gone: %v", name, err)
	}
	if len(f.state.Outbox()) != queued {
		f.t.Fatalf("%s: the refused retry queued an effect", name)
	}
}

func TestAnApprovalGivenAfterTheBlockLetsTheOwnerRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, rejected := refused(t)
	second := f.askAgain(rejected, "retry-1", nil)
	f.decide(second, approvals.DecisionApprove)

	result, err := f.service.Retry(ctx, f.retryBy(f.owner()))
	if err != nil {
		t.Fatalf("the retry was refused: %v", err)
	}
	f.deliver()
	wantEvents(t, result, core.EventRetryAuthorized)
	f.wantState(core.StateReady)
	// The step was the approval's one use.
	if got := f.statusOf(second.ID); got != approvals.StatusConsumed {
		t.Fatalf("the approval the retry used is %s", got)
	}
	_, err = f.approvals.Consume(ctx, second.ID, second.SubjectDigest, policyRevision)
	wantCode(t, err, core.CodeReplayed, "using the approval again")
	scope, err := f.service.ApprovedScope(ctx, issueRef)
	if err != nil || scope.Digest != second.SubjectDigest {
		t.Fatalf("the scope handed on must be the one approved: %+v, %v", scope, err)
	}
	if !f.hasLabel(github.LabelApproved) || f.hasLabel(github.LabelBlocked) {
		t.Fatal("the issue does not show that it is approved and no longer blocked")
	}
	record, _ := f.state.Record(issueRef)
	if record.State != core.StateReady {
		t.Fatalf("record = %+v", record)
	}
	// The block is lifted: a later block of the issue is not this one.
	if _, err := f.service.ApprovalDenial(ctx, issueRef); !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("the lifted denial is still on record: %v", err)
	}
	if _, err := f.service.Retry(ctx, f.retryBy(f.owner())); err == nil {
		t.Fatal("an issue that is Ready was retried again")
	}
	wantEvents(t, f.reconcile())
}

func TestARetryNeedsAnApprovalOfThisScopeDecidedAfterTheBlock(t *testing.T) {
	t.Parallel()
	const otherScope core.Digest = "sha256:5555555555555555555555555555555555555555555555555555555555555555"
	// Each case leaves one more request in the approval inbox and says where
	// it must still stand after the refused retry.
	cases := map[string]struct {
		prepare func(f *factory, rejected approvals.Request) approvals.Request
		stays   approvals.Status
	}{
		"a new request nobody decided": {func(f *factory, r approvals.Request) approvals.Request {
			return f.askAgain(r, "retry-1", nil)
		}, approvals.StatusPending},
		"a new request the Owner rejected": {func(f *factory, r approvals.Request) approvals.Request {
			again := f.askAgain(r, "retry-1", nil)
			f.decide(again, approvals.DecisionReject)
			return again
		}, approvals.StatusRejected},
		"an approval of another scope": {func(f *factory, r approvals.Request) approvals.Request {
			again := f.askAgain(r, "retry-1", func(a *approvals.Request) { a.SubjectDigest = otherScope })
			f.decide(again, approvals.DecisionApprove)
			return again
		}, approvals.StatusApproved},
		"an approval of another action": {func(f *factory, r approvals.Request) approvals.Request {
			again := f.askAgain(r, "retry-1", func(a *approvals.Request) { a.Action = "release.publish" })
			f.decide(again, approvals.DecisionApprove)
			return again
		}, approvals.StatusApproved},
		"an approval about another issue": {func(f *factory, r approvals.Request) approvals.Request {
			again := f.askAgain(r, "retry-1", func(a *approvals.Request) { a.IssueID.Number++ })
			f.decide(again, approvals.DecisionApprove)
			return again
		}, approvals.StatusApproved},
		"an approval given under a policy since replaced": {func(f *factory, r approvals.Request) approvals.Request {
			again := f.askAgain(r, "retry-1", nil)
			f.decide(again, approvals.DecisionApprove)
			f.policy.set(func(s *policy.Snapshot) { s.Revision = newerPolicy }, nil)
			return again
		}, approvals.StatusApproved},
		"an approval that lapsed before the retry": {func(f *factory, r approvals.Request) approvals.Request {
			again := f.askAgain(r, "retry-1", nil)
			f.decide(again, approvals.DecisionApprove)
			f.clock.Advance(intake.DefaultApprovalTTL)
			return again
		}, approvals.StatusApproved},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, rejected := refused(t)
			left := tc.prepare(f, rejected)
			queued := len(f.state.Outbox())
			_, err := f.service.Retry(context.Background(), f.retryBy(f.owner()))
			if err == nil {
				t.Fatal("the retry went through")
			}
			f.wantStillBlocked(name, queued)
			if got := f.statusOf(left.ID); got != tc.stays {
				t.Fatalf("the request became %s, want %s", got, tc.stays)
			}
		})
	}
}

func TestAnApprovalDecidedBeforeTheBlockDoesNotLiftIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, waiting := proposed(t)
	// The Owner approves a second request for the same scope, and then
	// rejects the one the issue waits on.
	earlier := f.askAgain(waiting, "earlier-1", nil)
	f.decide(earlier, approvals.DecisionApprove)
	f.clock.Advance(time.Minute)
	f.decide(waiting, approvals.DecisionReject)
	f.reconcile()
	f.wantState(core.StateBlocked)
	f.clock.Advance(time.Hour)
	queued := len(f.state.Outbox())

	_, err := f.service.Retry(ctx, f.retryBy(f.owner()))
	wantCode(t, err, core.CodeGuardFailed, "a retry on an approval older than the block")
	f.wantStillBlocked("an approval older than the block", queued)
	if got := f.statusOf(earlier.ID); got != approvals.StatusApproved {
		t.Fatalf("the refused retry spent the approval: %s", got)
	}

	// An approval given now does lift it, and the older one stays unspent.
	fresh := f.askAgain(waiting, "retry-1", nil)
	f.decide(fresh, approvals.DecisionApprove)
	if _, err := f.service.Retry(ctx, f.retryBy(f.owner())); err != nil {
		t.Fatalf("the retry on a fresh approval was refused: %v", err)
	}
	f.wantState(core.StateReady)
	if f.statusOf(fresh.ID) != approvals.StatusConsumed || f.statusOf(earlier.ID) != approvals.StatusApproved {
		t.Fatalf("spent the wrong approval: fresh %s, earlier %s", f.statusOf(fresh.ID), f.statusOf(earlier.ID))
	}
}

func TestOnlyAnAuthorizedPersonOrServiceCanRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := map[string]func(*intake.RetryRequest){
		"an agent, even one the rules would allow": func(r *intake.RetryRequest) {
			r.Actor.Kind, r.Actor.Roles = core.ActorAgent, []core.Role{core.RoleAgent, core.RoleOwner}
		},
		"an actor of unknown kind":         func(r *intake.RetryRequest) { r.Actor.Kind = "bot" },
		"nobody":                           func(r *intake.RetryRequest) { r.Actor = core.Actor{} },
		"a person the rules deny":          func(r *intake.RetryRequest) { r.Decision = core.AuthzDecision{Effect: core.EffectDeny} },
		"a person who needs an approval":   func(r *intake.RetryRequest) { r.Decision = core.AuthzDecision{Effect: core.EffectRequireApproval} },
		"a person whose answer is missing": func(r *intake.RetryRequest) { r.Decision = core.AuthzDecision{} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, rejected := refused(t)
			second := f.askAgain(rejected, "retry-1", nil)
			f.decide(second, approvals.DecisionApprove)
			queued := len(f.state.Outbox())

			request := f.retryBy(f.owner())
			change(&request)
			_, err := f.service.Retry(ctx, request)
			wantCode(t, err, core.CodeGuardFailed, name)
			f.wantStillBlocked(name, queued)
			if got := f.statusOf(second.ID); got != approvals.StatusApproved {
				t.Fatalf("the refused retry spent the approval: %s", got)
			}
		})
	}

	// A service the rules allow may ask, like a person.
	f, rejected := refused(t)
	f.decide(f.askAgain(rejected, "retry-1", nil), approvals.DecisionApprove)
	service := core.Actor{ID: "service:control", Kind: core.ActorService, Surface: core.SurfaceAPI}
	if _, err := f.service.Retry(ctx, f.retryBy(service)); err != nil {
		t.Fatalf("an authorized service was refused: %v", err)
	}
	f.wantState(core.StateReady)
}

func TestAScopeThatNowLiesInsideTheDelegationNeedsNoNewApproval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFactory(t)
	// A bug report by someone without write access waits for the Owner, and
	// nobody decides in time.
	f.open(outsider, completeBody, github.LabelReady, github.LabelKindBug)
	f.reconcile()
	request := f.waiting()
	f.clock.Advance(intake.DefaultApprovalTTL)
	f.reconcile()
	f.wantState(core.StateBlocked)
	f.clock.Advance(time.Hour)
	queued := len(f.state.Outbox())

	_, err := f.service.Retry(ctx, f.retryBy(f.owner()))
	wantCode(t, err, core.CodeGuardFailed, "a retry while the scope still needs the Owner")
	f.wantStillBlocked("a scope outside the delegation", queued)

	// The author is given write access. GitHub says so, not the issue.
	f.github.edit(issueRef, func(issue *github.Issue) { issue.AuthorAssociation = github.AssociationMember })
	result, err := f.service.Retry(ctx, f.retryBy(f.owner()))
	if err != nil {
		t.Fatalf("the retry of a delegated scope was refused: %v", err)
	}
	f.deliver()
	wantEvents(t, result, core.EventRetryAuthorized)
	f.wantState(core.StateReady)
	if got := f.statusOf(request.ID); got != approvals.StatusExpired {
		t.Fatalf("the lapsed request became %s", got)
	}
	if scope, err := f.service.ApprovedScope(ctx, issueRef); err != nil || scope.Digest != request.SubjectDigest {
		t.Fatalf("the scope handed on = %+v, %v", scope, err)
	}
	if !f.hasLabel(github.LabelApproved) || f.hasLabel(github.LabelBlocked) {
		t.Fatal("the issue does not show that it is approved and no longer blocked")
	}
}

func TestIntakeRetriesOnlyIssuesItBlockedForWantOfAnApproval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFactory(t)
	f.open(member, completeBody, github.LabelReady, github.LabelKindBug)
	f.reconcile()
	f.wantState(core.StateReady)
	elsewhere := f.retryBy(f.owner())
	elsewhere.Issue.Number = 99
	for name, request := range map[string]intake.RetryRequest{"an issue that is Ready": f.retryBy(f.owner()), "an unknown issue": elsewhere} {
		_, err := f.service.Retry(ctx, request)
		wantCode(t, err, core.CodeNotFound, name)
	}
	f.wantState(core.StateReady)

	// Without a policy in force nothing is retried either.
	g, rejected := refused(t)
	g.decide(g.askAgain(rejected, "retry-1", nil), approvals.DecisionApprove)
	g.policy.set(nil, core.NewError(core.CodePolicyHold, "the policy revision is not approved"))
	_, err := g.service.Retry(ctx, g.retryBy(g.owner()))
	wantCode(t, err, core.CodePolicyHold, "a retry under a policy hold")
	g.wantState(core.StateBlocked)
}
