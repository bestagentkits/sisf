package release

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/audit"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// approvalDesk is the approval service of the world: a real approval store
// in the operational store, signing with the approval service's key.
type approvalDesk struct {
	w     *world
	store *approvals.KVStore
	seq   int
}

func newApprovalDesk(t *testing.T, w *world) *approvalDesk {
	t.Helper()
	signer := w.keys.delegateTo(t, delegation.PurposeApproval, delegation.HolderApprovalService)
	store, err := approvals.NewStore(approvals.Config{
		State: w.store, FactoryID: testFactory, Signer: signer, Verifier: w.keys.verifier,
		Audit: audit.New(w.store, w.clock), Clock: w.clock,
	})
	if err != nil {
		t.Fatalf("approval store: %v", err)
	}
	w.cfg.Approvals = store
	w.cfg.Policy = func(context.Context) (core.CommitSHA, error) { return commitSHA, nil }
	return &approvalDesk{w: w, store: store}
}

// file creates a request for action on subject and, unless decision is
// empty, records the Owner's decision on it. It returns the request ID.
func (d *approvalDesk) file(action string, subject core.Digest, decision approvals.Decision) string {
	d.w.t.Helper()
	d.seq++
	ctx, now := context.Background(), d.w.clock.Now()
	nonce, err := approvals.NewNonce()
	if err != nil {
		d.w.t.Fatalf("nonce: %v", err)
	}
	id := "apr-" + string(rune('a'+d.seq))
	_, err = d.store.Create(ctx, approvals.Request{
		ID: id, FactoryID: testFactory, Action: action, SubjectDigest: subject, PolicyRevision: commitSHA,
		Nonce: nonce, ExpiresAt: now.Add(time.Hour), RequestedBy: "service:control",
		RiskSummary: "Restore the app to its last known good release", Status: approvals.StatusPending, CreatedAt: now,
	})
	if err != nil {
		d.w.t.Fatalf("create approval request: %v", err)
	}
	if decision == "" {
		return id
	}
	owner := core.Actor{ID: "user:owner", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner}, Surface: core.SurfaceDashboard}
	_, err = d.store.Decide(approvals.WithApprover(ctx, owner), approvals.Approval{
		RequestID: id, FactoryID: testFactory, Action: action, SubjectDigest: subject, PolicyRevision: commitSHA,
		Nonce: nonce, Decision: decision, ActorID: owner.ID, ActorKind: owner.Kind, Surface: owner.Surface,
	})
	if err != nil {
		d.w.t.Fatalf("decide approval request: %v", err)
	}
	return id
}

// brokenRelease makes another build than the LKG serve the unit.
func brokenRelease(w *world) {
	w.backend.stable = core.ReleaseDigest(otherBuild)
	w.backend.existing[core.ReleaseDigest(otherBuild)] = true
}

func rollbackRequest(ref string) supervisor.RollbackRequest {
	return supervisor.RollbackRequest{FactoryID: testFactory, Unit: testUnit, Reason: "the release broke checkout", ApprovalRef: ref}
}

func TestAnOwnerApprovedRollbackRestoresTheLKGOnce(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	desk := newApprovalDesk(t, w)
	brokenRelease(w)
	ref := desk.file(RollbackAction, RollbackSubject(testFactory, testUnit, lkgDigest), approvals.DecisionApprove)
	p := w.pipeline()

	report, err := p.OwnerRollback(context.Background(), rollbackRequest(ref))
	if err != nil {
		t.Fatalf("OwnerRollback: %v", err)
	}
	if report.From != core.ReleaseDigest(otherBuild) || report.RestoredTo != lkgDigest || report.Observed != lkgDigest || !report.Healthy {
		t.Fatalf("report %+v, want the LKG observed running in place of the broken release", report)
	}
	brokenRelease(w)
	if _, err := p.OwnerRollback(context.Background(), rollbackRequest(ref)); !core.HasCode(err, core.CodeReplayed) {
		t.Fatalf("a second use of the approval = %v, want REPLAYED", err)
	}
	if _, rollbacks, _, data := w.backend.snapshot(); len(rollbacks) != 1 || data != "customer-data-v1" {
		t.Fatalf("rollbacks %v data %q, want one rollback and the data untouched", rollbacks, data)
	}
}

func TestARollbackWithoutAValidSingleUseApprovalIsRefused(t *testing.T) {
	t.Parallel()
	subject := RollbackSubject(testFactory, testUnit, lkgDigest)
	cases := map[string]struct {
		ref  func(d *approvalDesk) string
		code core.ErrorCode
	}{
		"no approval":          {func(*approvalDesk) string { return "" }, core.CodeApprovalRequired},
		"an unknown approval":  {func(*approvalDesk) string { return "apr-unknown" }, core.CodeNotFound},
		"an undecided request": {func(d *approvalDesk) string { return d.file(RollbackAction, subject, "") }, core.CodeApprovalRequired},
		"a rejected request":   {func(d *approvalDesk) string { return d.file(RollbackAction, subject, approvals.DecisionReject) }, core.CodePermissionDenied},
		"another rollback target": {func(d *approvalDesk) string {
			return d.file(RollbackAction, RollbackSubject(testFactory, testUnit, core.ReleaseDigest(otherBuild)), approvals.DecisionApprove)
		}, core.CodeApprovalRequired},
		"an approval of another action": {func(d *approvalDesk) string {
			return d.file("policy.change", subject, approvals.DecisionApprove)
		}, core.CodeApprovalRequired},
		"an expired approval": {func(d *approvalDesk) string {
			ref := d.file(RollbackAction, subject, approvals.DecisionApprove)
			d.w.clock.Advance(2 * time.Hour)
			return ref
		}, core.CodeExpired},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			desk := newApprovalDesk(t, w)
			brokenRelease(w)
			_, err := w.pipeline().OwnerRollback(context.Background(), rollbackRequest(c.ref(desk)))
			if !core.HasCode(err, c.code) {
				t.Fatalf("OwnerRollback = %v, want %s", err, c.code)
			}
			if _, rollbacks, _, _ := w.backend.snapshot(); len(rollbacks) != 0 {
				t.Fatalf("a refused rollback still rolled back: %v", rollbacks)
			}
		})
	}
}

func TestARollbackForAnotherFactoryOrWithoutApprovalsIsRefused(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if _, err := w.pipeline().OwnerRollback(context.Background(), rollbackRequest("apr-x")); !core.HasCode(err, core.CodeNotSupported) {
		t.Fatalf("without an approval store = %v, want NOT_SUPPORTED", err)
	}
	newApprovalDesk(t, w)
	req := rollbackRequest("apr-x")
	req.FactoryID = "other-factory"
	if _, err := w.pipeline().OwnerRollback(context.Background(), req); !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("for another factory = %v, want PERMISSION_DENIED", err)
	}
}
