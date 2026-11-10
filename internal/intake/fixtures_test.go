package intake_test

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state"
)

const policyRevision core.CommitSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

var (
	testNow = time.Date(2026, 11, 10, 7, 0, 0, 0, time.UTC)
	// issueRef is an issue of the repository the sample manifest declares.
	issueRef = core.IssueRef{Owner: "acme", Repo: "hello-app", Number: 7}
	repo     = github.RepoRef{Owner: "acme", Name: "hello-app"}
)

// People who appear in the tests. What they may do follows from how GitHub
// says they relate to the repository, never from what they write.
var (
	member   = person{github.User{ID: 11, Login: "maintainer"}, github.AssociationMember}
	outsider = person{github.User{ID: 99, Login: "passer-by"}, github.AssociationNone}
	// factoryAccount is the account the factory comments as. It is an
	// ordinary collaborator, not a bot, as it is when a developer runs the
	// factory with their own credentials.
	factoryAccount = person{github.User{ID: 1, Login: "factory"}, github.AssociationOwner}
)

type person struct {
	user        github.User
	association github.AuthorAssociation
}

// A complete request: it says what to change and how to tell it is done.
const completeBody = "The health endpoint returns 500 when the database is slow.\n\n" +
	"### Definition of Done\n- /healthz answers 200 within one second while the database is slow\n- a regression test covers it\n"

// policyStub hands out one policy snapshot, or a hold.
type policyStub struct {
	mu       sync.Mutex
	snapshot *policy.Snapshot
	err      error
	reads    int
}

func (p *policyStub) Current(context.Context) (*policy.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reads++
	if p.err != nil {
		return nil, p.err
	}
	copied := *p.snapshot
	return &copied, nil
}

func (p *policyStub) set(change func(*policy.Snapshot), err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if change != nil {
		change(p.snapshot)
	}
	p.err = err
}

// factory is intake wired to in-memory state, an in-memory GitHub, a real
// approval store and a clock the test moves.
type factory struct {
	t         *testing.T
	clock     *intaketest.Clock
	state     *intaketest.State
	audit     *intaketest.AuditLog
	approvals *approvals.KVStore
	policy    *policyStub
	github    *fakeGitHub
	service   *intake.Service
}

// newFactory builds the factory under the sample manifest of this repository
// and a small feature catalogue.
func newFactory(t *testing.T) *factory {
	t.Helper()
	return newFactoryWith(t, func(store *approvals.KVStore) intake.Approvals { return store })
}

// newFactoryWith builds the factory with wrap put between intake and the
// approval store, for tests of an approval store that misbehaves.
func newFactoryWith(t *testing.T, wrap func(*approvals.KVStore) intake.Approvals) *factory {
	t.Helper()
	m, err := manifest.ParseFile(filepath.Join("..", "..", ".sisf", "factory.yaml"))
	if err != nil {
		t.Fatalf("the sample manifest cannot be read: %v", err)
	}
	clock := intaketest.NewClock(testNow)
	keys := intaketest.NewKeys("approval-key-1")
	f := &factory{t: t, clock: clock, state: intaketest.NewState(clock), audit: &intaketest.AuditLog{},
		github: &fakeGitHub{now: clock.Now, issues: map[core.IssueRef]github.Issue{}},
		policy: &policyStub{snapshot: &policy.Snapshot{Revision: policyRevision, Manifest: m, DecisionSchemaVersion: 1,
			Catalog: policy.Catalog{Features: []policy.Feature{{Name: "Health endpoint"}, {Name: "Dashboard"}}}}},
	}
	f.approvals, err = approvals.NewStore(approvals.Config{State: f.state, FactoryID: m.FactoryID,
		Signer: keys, Verifier: keys, Audit: f.audit, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	f.service, err = intake.New(intake.Config{State: f.state, Policy: f.policy, Issues: f.github, Approvals: wrap(f.approvals), Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// open files an issue by author with the given body and labels.
func (f *factory) open(author person, body string, labels ...string) {
	f.t.Helper()
	issue := github.Issue{Ref: issueRef, Title: "Health check fails under load", Body: body, State: github.IssueOpen,
		Author: author.user, AuthorAssociation: author.association, CreatedAt: f.clock.Now(), UpdatedAt: f.clock.Now()}
	for _, name := range labels {
		issue.Labels = append(issue.Labels, github.Label{Name: name})
	}
	f.github.mu.Lock()
	f.github.issues[issueRef] = issue
	f.github.mu.Unlock()
}

// reconcile runs intake on the issue and then delivers what it queued for
// GitHub, as the running factory does.
func (f *factory) reconcile() intake.Result {
	f.t.Helper()
	result, err := f.service.Reconcile(context.Background(), issueRef)
	if err != nil {
		f.t.Fatalf("intake failed: %v", err)
	}
	f.deliver()
	return result
}

// deliver sends every queued effect to GitHub and marks it sent.
func (f *factory) deliver() {
	f.t.Helper()
	ctx := context.Background()
	var pending []state.OutboxMessage
	if err := f.state.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		pending, err = tx.PendingOutbox(ctx, 0)
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	for _, m := range pending {
		if err := intake.Deliver(ctx, f.github, m); err != nil {
			f.t.Fatalf("effect %s could not be delivered: %v", m.Key, err)
		}
		if err := f.state.Update(ctx, func(ctx context.Context, tx state.Tx) error { return tx.MarkOutboxSent(ctx, m.ID) }); err != nil {
			f.t.Fatal(err)
		}
	}
}

// decide records the Owner's decision on request r.
func (f *factory) decide(r approvals.Request, decision approvals.Decision) {
	f.t.Helper()
	if _, err := f.approvals.Submit(context.Background(), f.owner(), answer(r, decision)); err != nil {
		f.t.Fatalf("the Owner's decision could not be recorded: %v", err)
	}
}

// waiting returns the one approval request the issue waits on.
func (f *factory) waiting() approvals.Request {
	f.t.Helper()
	pending := f.inbox(approvals.StatusPending)
	if len(pending) != 1 {
		f.t.Fatalf("want one pending approval request, got %+v", pending)
	}
	return pending[0]
}

// event is a webhook delivery about the issue as GitHub shows it now.
func (f *factory) event(kind, action string, change func(*github.WebhookEvent)) github.WebhookEvent {
	f.t.Helper()
	issue, err := f.github.GetIssue(context.Background(), issueRef)
	if err != nil {
		f.t.Fatal(err)
	}
	ev := github.WebhookEvent{DeliveryID: "delivery-1", Type: kind, Action: action, InstallationID: 42, Repo: repo,
		Sender: member.user, Issue: &issue, ReceivedAt: f.clock.Now()}
	if change != nil {
		change(&ev)
	}
	return ev
}

// handle passes a webhook delivery to intake and delivers what it queued.
func (f *factory) handle(ev github.WebhookEvent) intake.Result {
	f.t.Helper()
	result, err := f.service.HandleEvent(context.Background(), ev)
	if err != nil {
		f.t.Fatalf("intake failed on the event: %v", err)
	}
	f.deliver()
	return result
}

// spent is what the factory has read and queued so far: reads of GitHub,
// reads of the policy and queued effects. An issue that costs nothing
// leaves all three unchanged.
type spent struct{ github, policy, effects int }

func (f *factory) spent() spent {
	f.policy.mu.Lock()
	defer f.policy.mu.Unlock()
	return spent{github: f.github.readCount(), policy: f.policy.reads, effects: len(f.state.Outbox())}
}

// stateOf returns the lifecycle state of the issue, or "" without a record.
func (f *factory) stateOf() core.State {
	record, _ := f.state.Record(issueRef)
	return record.State
}

// wantState fails the test unless the issue is in state.
func (f *factory) wantState(want core.State) {
	f.t.Helper()
	if got := f.stateOf(); got != want {
		f.t.Fatalf("the issue is in %q, want %q", got, want)
	}
}

// inbox returns the approval requests of the issue with the given statuses.
func (f *factory) inbox(statuses ...approvals.Status) []approvals.Request {
	f.t.Helper()
	requests, err := f.approvals.List(context.Background(), approvals.Filter{IssueID: issueRef, Statuses: statuses})
	if err != nil {
		f.t.Fatal(err)
	}
	return requests
}

// owner is the person who owns the factory, identity just confirmed.
func (f *factory) owner() core.Actor {
	return core.Actor{ID: "user:owner-1", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner},
		Surface: core.SurfaceDashboard, StepUpAt: f.clock.Now()}
}

// answer is what an approver who read request r sends back.
func answer(r approvals.Request, decision approvals.Decision) approvals.Submission {
	return approvals.Submission{RequestID: r.ID, Nonce: r.Nonce, SubjectDigest: r.SubjectDigest,
		PolicyRevision: r.PolicyRevision, Decision: decision}
}

// hasLabel reports whether the issue on GitHub carries the label.
func (f *factory) hasLabel(name string) bool {
	issue, err := f.github.GetIssue(context.Background(), issueRef)
	if err != nil {
		f.t.Fatal(err)
	}
	return issue.HasLabel(name)
}

// count returns how many factory comments contain text.
func (f *factory) count(text string) int {
	n := 0
	for _, body := range f.github.factoryComments(issueRef) {
		if strings.Contains(body, text) {
			n++
		}
	}
	return n
}

// wantCode fails the test unless err carries code.
func wantCode(t *testing.T, err error, code core.ErrorCode, what string) {
	t.Helper()
	if !core.HasCode(err, code) {
		t.Fatalf("%s: want %s, got %v", what, code, err)
	}
}

// wantEvents fails the test unless the call applied exactly these events.
func wantEvents(t *testing.T, result intake.Result, want ...core.Event) {
	t.Helper()
	if !slices.Equal(result.Events, want) {
		t.Fatalf("events = %v (skipped: %q), want %v", result.Events, result.Skipped, want)
	}
}
