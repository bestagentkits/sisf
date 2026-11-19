package release

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/managedapp/changegate"
)

// gateFake judges every change the same way and counts the judgements.
type gateFake struct {
	mu      sync.Mutex
	verdict changegate.Verdict
	err     error
	judged  int
}

func (g *gateFake) Judge(context.Context, core.CommitSHA, core.CommitSHA, []string) (changegate.Verdict, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.judged++
	return g.verdict, g.err
}

// coreUpgrade is the verdict of a candidate that moves WordPress Core to
// 7.0.8: a change only the Owner releases.
func coreUpgrade() *gateFake {
	return &gateFake{verdict: changegate.Verdict{Owner: true, Reasons: []string{"core wordpress 6.9.1 -> 7.0.8 is a core-upgrade change"}}}
}

// decide records the Owner's decision on the request the pipeline filed.
func (d *approvalDesk) decide(id string, decision approvals.Decision) {
	d.w.t.Helper()
	ctx := context.Background()
	r, err := d.store.Get(ctx, id)
	if err != nil {
		d.w.t.Fatalf("read approval request %s: %v", id, err)
	}
	owner := core.Actor{ID: "user:owner", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner}, Surface: core.SurfaceDashboard}
	_, err = d.store.Decide(approvals.WithApprover(ctx, owner), approvals.Approval{
		RequestID: id, FactoryID: r.FactoryID, IssueID: r.IssueID, Action: r.Action, SubjectDigest: r.SubjectDigest,
		PolicyRevision: r.PolicyRevision, Nonce: r.Nonce, Decision: decision, ActorID: owner.ID, ActorKind: owner.Kind, Surface: owner.Surface,
	})
	if err != nil {
		d.w.t.Fatalf("decide approval request %s: %v", id, err)
	}
}

func (w *world) staged() []core.ReleaseDigest {
	_, _, staged, _ := w.backend.snapshot()
	return staged
}

// heldForOwner asserts the release held, staged nothing and filed one
// request for the Owner, which it returns.
func heldForOwner(t *testing.T, w *world, out Outcome, err error) string {
	t.Helper()
	if !core.HasCode(err, core.CodeReleaseHold) || out.Result != ResultHeld {
		t.Fatalf("Release = %+v, %v; want held for the Owner's approval", out, err)
	}
	if staged := w.staged(); len(staged) != 0 {
		t.Fatalf("%v was staged without the Owner's approval", staged)
	}
	var e *core.Error
	if !asCoreError(err, &e) || e.Details["approval_id"] == "" {
		t.Fatalf("the hold %v names no approval request", err)
	}
	return e.Details["approval_id"]
}

func asCoreError(err error, into **core.Error) bool {
	for err != nil {
		if e, ok := err.(*core.Error); ok {
			*into = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// A candidate the change gate gives to the Owner, such as a Core upgrade,
// stages nothing until the Owner approves exactly its build, and the
// release then goes ahead once on that approval.
func TestAnOwnerChangeIsReleasedOnlyOnTheOwnersApprovalOfItsBuild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	desk := newApprovalDesk(t, w)
	w.cfg.ChangeGate = coreUpgrade()
	issue, run := issueRef(301), core.RunID("run-301")
	w.seedReleaseCandidate(issue, run, firstBuild)
	p := w.pipeline()

	out, err := p.Release(ctx, w.request(issue, run, firstBuild))
	id := heldForOwner(t, w, out, err)
	if !strings.Contains(out.Reason, "7.0.8") {
		t.Fatalf("the hold %q does not say why the change is the Owner's", out.Reason)
	}
	filed, err := desk.store.Get(ctx, id)
	if err != nil || filed.Action != ManagedChangeAction || filed.IssueID != issue ||
		filed.SubjectDigest != ManagedChangeSubject(testFactory, testUnit, core.ReleaseDigest(firstBuild)) {
		t.Fatalf("filed %+v, %v; want a request bound to the issue and the build", filed, err)
	}
	// Asking again files nothing new while the request waits.
	out, err = p.Release(ctx, w.request(issue, run, firstBuild))
	if again := heldForOwner(t, w, out, err); again != id {
		t.Fatalf("a second pass filed %s beside %s", again, id)
	}

	desk.decide(id, approvals.DecisionApprove)
	out, err = p.Release(ctx, w.request(issue, run, firstBuild))
	if err != nil || out.Result != ResultClosed {
		t.Fatalf("Release after the approval = %+v, %v; want closed", out, err)
	}
	if spent, _ := desk.store.Get(ctx, id); spent.Status != approvals.StatusConsumed {
		t.Fatalf("the approval is %s after the release, want consumed", spent.Status)
	}
}

// An approval of another build, of another action, or one the Owner
// rejected releases nothing.
func TestAnOwnerChangeIsNotReleasedOnAnotherApproval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("another build", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		desk := newApprovalDesk(t, w)
		w.cfg.ChangeGate = coreUpgrade()
		desk.file(ManagedChangeAction, ManagedChangeSubject(testFactory, testUnit, core.ReleaseDigest(otherBuild)), approvals.DecisionApprove)
		issue, run := issueRef(302), core.RunID("run-302")
		w.seedReleaseCandidate(issue, run, firstBuild)
		out, err := w.pipeline().Release(ctx, w.request(issue, run, firstBuild))
		heldForOwner(t, w, out, err)
	})

	t.Run("a rollback approval", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		desk := newApprovalDesk(t, w)
		w.cfg.ChangeGate = coreUpgrade()
		desk.file(RollbackAction, ManagedChangeSubject(testFactory, testUnit, core.ReleaseDigest(firstBuild)), approvals.DecisionApprove)
		issue, run := issueRef(303), core.RunID("run-303")
		w.seedReleaseCandidate(issue, run, firstBuild)
		out, err := w.pipeline().Release(ctx, w.request(issue, run, firstBuild))
		heldForOwner(t, w, out, err)
	})

	t.Run("a rejection", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		desk := newApprovalDesk(t, w)
		w.cfg.ChangeGate = coreUpgrade()
		issue, run := issueRef(304), core.RunID("run-304")
		w.seedReleaseCandidate(issue, run, firstBuild)
		p := w.pipeline()
		out, err := p.Release(ctx, w.request(issue, run, firstBuild))
		desk.decide(heldForOwner(t, w, out, err), approvals.DecisionReject)
		out, err = p.Release(ctx, w.request(issue, run, firstBuild))
		if !core.HasCode(err, core.CodeApprovalRequired) || out.Result != ResultRefused {
			t.Fatalf("Release after a rejection = %+v, %v; want refused", out, err)
		}
		if staged := w.staged(); len(staged) != 0 {
			t.Fatalf("%v was staged after the Owner rejected it", staged)
		}
	})
}

// An approval is spent only when the release starts: a release that holds
// after it was judged keeps the approval for the next pass.
func TestAnApprovalOutlivesAHoldBeforeTheReleaseStarts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	desk := newApprovalDesk(t, w)
	w.cfg.ChangeGate = coreUpgrade()
	issue, run := issueRef(305), core.RunID("run-305")
	w.seedReleaseCandidate(issue, run, firstBuild)
	p := w.pipeline()
	out, err := p.Release(ctx, w.request(issue, run, firstBuild))
	id := heldForOwner(t, w, out, err)
	desk.decide(id, approvals.DecisionApprove)

	w.backend.failObserve(true)
	if out, err := p.Release(ctx, w.request(issue, run, firstBuild)); out.Result != ResultHeld || err == nil {
		t.Fatalf("Release with the way back unseen = %+v, %v; want held", out, err)
	}
	if r, _ := desk.store.Get(ctx, id); r.Status != approvals.StatusApproved {
		t.Fatalf("the approval is %s after a hold, want still approved", r.Status)
	}
	w.backend.failObserve(false)
	if out, err := p.Release(ctx, w.request(issue, run, firstBuild)); err != nil || out.Result != ResultClosed {
		t.Fatalf("Release on the next pass = %+v, %v; want closed", out, err)
	}
}

// A change the factory may release alone needs no approval; a gate that
// cannot read the change judged nothing and the release is tried again;
// and an Owner change on a supervisor that cannot take approvals is
// refused, never released.
func TestTheChangeGateFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("a factory change", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		gate := &gateFake{}
		w.cfg.ChangeGate = gate
		issue, run := issueRef(306), core.RunID("run-306")
		w.seedReleaseCandidate(issue, run, firstBuild)
		out, err := w.pipeline().Release(ctx, w.request(issue, run, firstBuild))
		if err != nil || out.Result != ResultClosed || gate.judged != 1 {
			t.Fatalf("Release = %+v, %v, judged %d; want closed after one judgement", out, err, gate.judged)
		}
	})

	t.Run("an outage", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.cfg.ChangeGate = &gateFake{err: core.NewError(core.CodeNotFound, "GitHub answered 404 for the lockfile")}
		issue, run := issueRef(307), core.RunID("run-307")
		w.seedReleaseCandidate(issue, run, firstBuild)
		out, err := w.pipeline().Release(ctx, w.request(issue, run, firstBuild))
		if err == nil || out.Result == ResultRefused || out.Result == ResultClosed || len(w.staged()) != 0 {
			t.Fatalf("Release = %+v, %v; want nothing judged or staged", out, err)
		}
		w.requireState(issue, core.StateReleaseCandidate)
	})

	t.Run("no approvals", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.cfg.ChangeGate = coreUpgrade()
		issue, run := issueRef(308), core.RunID("run-308")
		w.seedReleaseCandidate(issue, run, firstBuild)
		out, err := w.pipeline().Release(ctx, w.request(issue, run, firstBuild))
		if !core.HasCode(err, core.CodeApprovalRequired) || out.Result != ResultRefused || len(w.staged()) != 0 {
			t.Fatalf("Release = %+v, %v; want refused with nothing staged", out, err)
		}
	})
}

// failObserve makes what runs unobservable, or observable again.
func (b *fakeBackend) failObserve(fail bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.observeErr = nil
	if fail {
		b.observeErr = core.NewError(core.CodeUnavailable, "the Docker endpoint does not answer")
	}
}
