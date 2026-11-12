package policy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

const (
	// ChangeRequestTTL is how long a request to change the policy in force
	// stays open.
	ChangeRequestTTL = 7 * 24 * time.Hour
	// maxChangeRequests bounds how often one change is put to the Owner
	// again after an answer that was not an approval.
	maxChangeRequests = 20
)

// RequestChange files the approval request that replacing the policy in
// force with revision needs, and returns it. Approve spends an approval of
// it: the request names this factory, the action ActionApproveRevision, the
// content of the foundation files at revision and the revision it replaces.
//
// Asking again while a request for the same change is open or approved
// returns that request; after a rejection, a lapse or a use, a new one is
// filed. Only a person holding the Owner role may ask (PERMISSION_DENIED
// otherwise), the files at revision must form a policy of this factory
// (POLICY_HOLD otherwise), and the change must replace something: before
// the first revision is approved, or for the revision in force, the answer
// is CONFLICT.
func (b *Baselines) RequestChange(ctx context.Context, actor core.Actor, revision core.CommitSHA) (approvals.Request, error) {
	if err := revision.Validate(); err != nil {
		return approvals.Request{}, err
	}
	if actor.ID == "" || actor.Kind != core.ActorHuman || !actor.HasRole(core.RoleOwner) {
		return approvals.Request{}, core.NewError(core.CodePermissionDenied,
			"only the person who owns the factory can ask to change the policy")
	}
	current, err := b.Current(ctx)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return approvals.Request{}, core.NewError(core.CodeConflict, "no policy is in force yet; the first revision needs no approval",
			core.NextAction{Action: "policy.approve", Description: "approve the first policy revision"})
	case err != nil:
		return approvals.Request{}, err
	case current.Revision == revision:
		return approvals.Request{}, core.NewError(core.CodeConflict, "this revision is already the policy in force")
	}
	snapshot, err := read(ctx, b.cfg.Source, revision)
	if err != nil {
		return approvals.Request{}, err
	}
	if snapshot.Manifest.FactoryID != b.cfg.FactoryID {
		return approvals.Request{}, hold(reasonInvalid, "%s at %s declares factory %q, not this factory",
			PathFactory, revision, string(snapshot.Manifest.FactoryID))
	}

	base := fmt.Sprintf("policy:%s:%s:%s", string(revision)[:12],
		strings.TrimPrefix(string(snapshot.Digest), "sha256:")[:16], string(current.Revision)[:12])
	for n := 1; n <= maxChangeRequests; n++ {
		id := base
		if n > 1 {
			id = fmt.Sprintf("%s:%d", base, n)
		}
		request, err := b.fileChange(ctx, actor, id, revision, snapshot.Digest, current.Revision)
		if err != nil {
			return approvals.Request{}, err
		}
		if request.Status == approvals.StatusPending || request.Status == approvals.StatusApproved {
			return request, nil
		}
	}
	return approvals.Request{}, core.NewError(core.CodeLimitExceeded,
		fmt.Sprintf("the Owner was asked %d times about this policy change", maxChangeRequests))
}

// fileChange files the request with the given ID, or returns the one already
// filed under it.
func (b *Baselines) fileChange(ctx context.Context, actor core.Actor, id string, revision core.CommitSHA,
	content core.Digest, replaced core.CommitSHA) (approvals.Request, error) {
	existing, err := b.cfg.Approvals.Get(ctx, id)
	switch {
	case err == nil && (existing.Action != ActionApproveRevision || existing.FactoryID != b.cfg.FactoryID ||
		existing.SubjectDigest != content || existing.PolicyRevision != replaced):
		return approvals.Request{}, core.NewError(core.CodeConflict, "another approval request already uses this id")
	case err == nil:
		return existing, nil
	case !core.HasCode(err, core.CodeNotFound):
		return approvals.Request{}, err
	}
	nonce, err := approvals.NewNonce()
	if err != nil {
		return approvals.Request{}, err
	}
	now := b.cfg.Clock.Now()
	created, err := b.cfg.Approvals.Create(ctx, approvals.Request{
		ID: id, FactoryID: b.cfg.FactoryID, Action: ActionApproveRevision, SubjectDigest: content,
		PolicyRevision: replaced, Nonce: nonce, ExpiresAt: now.Add(ChangeRequestTTL), RequestedBy: actor.ID,
		RiskSummary: fmt.Sprintf("Replaces the policy in force (%s) with revision %s: every rule agents, approvals and releases are judged by may change.",
			string(replaced)[:12], string(revision)[:12]),
		EvidenceRefs: []string{"policy:" + string(revision), "policy:" + string(replaced)},
		// Changing the rules is a decision for a person who has just
		// confirmed who they are.
		StepUp: true, Status: approvals.StatusPending, CreatedAt: now,
	})
	if core.HasCode(err, core.CodeAlreadyExists) {
		return b.cfg.Approvals.Get(ctx, id)
	}
	return created, err
}
