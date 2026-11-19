package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/managedapp/changegate"
)

// ManagedChangeAction is the approval action an Owner approves to have the
// factory release one build of a managed application whose change only the
// Owner may release, such as a Core upgrade or a new plugin.
const ManagedChangeAction = "releases.managed_change"

// Domain and version of the payload a managed change approval's subject
// digests.
const (
	managedChangeSubjectDomain  = "sisf.managed_change_subject"
	managedChangeSubjectVersion = 1
)

// maxChangeRequests bounds how many approval requests one build files in
// its life: each lapsed, superseded or used request moves to the next slot.
const maxChangeRequests = 20

// changeRequestTTL is how long the Owner has to decide a request the
// pipeline filed; a lapsed request is filed again on the next pass.
const changeRequestTTL = 72 * time.Hour

// ManagedChangeSubject is the subject digest of an approval to release the
// build digest as unit of factory. An approval of one build never covers
// another: a rebuilt candidate needs its own.
func ManagedChangeSubject(factory core.FactoryID, unit string, digest core.ReleaseDigest) core.Digest {
	payload := core.NewPayloadBuilder(managedChangeSubjectDomain, managedChangeSubjectVersion).
		Text("factory_id", string(factory)).Text("unit", unit).Text("release", string(digest)).Bytes()
	sum := sha256.Sum256(payload)
	return core.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

// ChangeGate judges, against the Owner-approved baseline at base, whether
// the change from base to candidate may be released by the factory alone.
// It reads what it judges from the repository at those commits, never from
// the candidate's own account. An error means nothing was judged.
type ChangeGate interface {
	Judge(ctx context.Context, base, candidate core.CommitSHA, changed []string) (changegate.Verdict, error)
}

// ChangeApprovals is the approval store as the change gate uses it: it
// files a request for the Owner, reads it and its decision, and spends it.
// The approval store of package approvals implements it.
type ChangeApprovals interface {
	ApprovalSpender
	Get(ctx context.Context, id string) (approvals.Request, error)
	Create(ctx context.Context, r approvals.Request) (approvals.Request, error)
	Decision(ctx context.Context, requestID string) (approvals.Approval, error)
}

// changeRequestID is the ID of the n-th request for subject. Its prefix
// keeps it apart from the requests the action service files.
func changeRequestID(subject core.Digest, n int) string {
	_, h, _ := strings.Cut(string(subject), ":")
	return fmt.Sprintf("rel-%s-%d", h[:32], n)
}

// ownerApproval is the approval a release of an Owner change goes ahead
// on: checked when the release is judged, spent only when it starts.
type ownerApproval struct {
	id      string
	subject core.Digest
	policy  core.CommitSHA
}

// judgeChange applies the change gate to a verified release. It returns
// the approval to spend when the change is the Owner's and the Owner
// approved exactly this build; it holds while the Owner has not decided,
// filing the request they decide on, and refuses a change the Owner
// rejected or a supervisor that cannot take an approval. Without a gate
// every change passes as before.
func (p *Pipeline) judgeChange(ctx context.Context, v verifiedRelease, changed []string) (*ownerApproval, error) {
	if p.cfg.ChangeGate == nil {
		return nil, nil
	}
	verdict, err := p.cfg.ChangeGate.Judge(ctx, v.receipt.BaselineSHA, v.receipt.CandidateSHA, changed)
	if err != nil {
		// An outage of what the gate reads judged nothing: tried again.
		return nil, core.NewError(core.CodeUnavailable, "the change could not be judged").WithCause(err)
	}
	if !verdict.Owner {
		return nil, nil
	}
	why := strings.Join(verdict.Reasons, "; ")
	desk, ok := p.cfg.Approvals.(ChangeApprovals)
	if !ok || p.cfg.Policy == nil {
		return nil, core.NewError(core.CodeApprovalRequired, "only the Owner may release this change ("+why+"), and this supervisor cannot take an Owner approval")
	}
	policy, err := p.cfg.Policy(ctx)
	if err != nil {
		return nil, core.NewError(core.CodePolicyHold, "the policy in force cannot be read").WithCause(err)
	}
	subject := ManagedChangeSubject(p.cfg.FactoryID, v.artifact.Unit, v.artifact.Digest)
	now := p.cfg.Clock.Now()
	for n := 1; n <= maxChangeRequests; n++ {
		id := changeRequestID(subject, n)
		r, err := desk.Get(ctx, id)
		switch {
		case core.HasCode(err, core.CodeNotFound):
			return nil, p.fileChangeRequest(ctx, desk, v, id, subject, policy, why)
		case err != nil:
			return nil, err
		case r.Action != ManagedChangeAction || r.SubjectDigest != subject || r.IssueID != v.issue:
			return nil, core.NewError(core.CodeTampered, "another approval request uses the id of this release's request")
		case r.PolicyRevision != policy:
			continue
		}
		switch r.Status {
		case approvals.StatusPending:
			if r.Expired(now) {
				continue
			}
			return nil, waitingForOwner(id, why)
		case approvals.StatusApproved:
			a, err := desk.Decision(ctx, id)
			if err != nil {
				return nil, err
			}
			if a.Authorizes(subject, policy, now) != nil {
				continue
			}
			return &ownerApproval{id: id, subject: subject, policy: policy}, nil
		case approvals.StatusRejected, approvals.StatusChangesRequested:
			return nil, core.NewError(core.CodeApprovalRequired, "the Owner did not approve releasing this change ("+why+")")
		}
	}
	return nil, core.NewError(core.CodeApprovalRequired, "the Owner was asked about this build too many times")
}

// fileChangeRequest files the request the Owner decides and holds the
// release for it. Another pass filing the same request at the same moment
// holds the same way.
func (p *Pipeline) fileChangeRequest(ctx context.Context, desk ChangeApprovals, v verifiedRelease, id string, subject core.Digest, policy core.CommitSHA, why string) error {
	nonce, err := approvals.NewNonce()
	if err != nil {
		return err
	}
	now := p.cfg.Clock.Now()
	_, err = desk.Create(ctx, approvals.Request{
		ID: id, FactoryID: p.cfg.FactoryID, IssueID: v.issue, Action: ManagedChangeAction, SubjectDigest: subject,
		PolicyRevision: policy, Nonce: nonce, ExpiresAt: now.Add(changeRequestTTL), RequestedBy: "service:supervisor",
		RiskSummary:  "Release " + string(v.artifact.Digest) + " of " + v.artifact.Unit + ": " + why,
		EvidenceRefs: []string{"release:" + string(v.artifact.Digest), "qa-receipt:" + string(v.receipt.CandidateSHA)},
		Status:       approvals.StatusPending, CreatedAt: now,
	})
	if err != nil && !core.HasCode(err, core.CodeAlreadyExists) {
		return err
	}
	return waitingForOwner(id, why)
}

func waitingForOwner(id, why string) error {
	e := holdf("only the Owner may release this change (%s); it waits for the Owner's decision on approval request %s", why, id)
	e.Details = map[string]string{"approval_id": id}
	return e
}

// spendOwnerApproval spends the approval just before the release starts,
// once nothing can hold it any more, so a hold keeps it for the next pass.
// The spent approval must be for this factory, issue and action.
func (p *Pipeline) spendOwnerApproval(ctx context.Context, v verifiedRelease, a *ownerApproval) error {
	if a == nil {
		return nil
	}
	approval, err := p.cfg.Approvals.Consume(ctx, a.id, a.subject, a.policy)
	if err == nil && (approval.Action != ManagedChangeAction || approval.FactoryID != p.cfg.FactoryID || approval.IssueID != v.issue) {
		err = core.NewError(core.CodeApprovalRequired, "the approval was given for another action")
	}
	return err
}
