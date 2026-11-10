package intake

import (
	"context"
	"fmt"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// maxRetryRequests bounds how many times the Owner can be asked again about
// one block. Each answer that is not an approval uses one.
const maxRetryRequests = 20

// RequestRetryApproval asks the Owner once more to approve the scope an
// issue was blocked with, because its first approval request was rejected,
// lapsed or went elsewhere. It is what the surface through which a retry is
// asked for calls when Retry is refused for want of an approval: the request
// it files is the approval Retry then looks for.
//
// The request is about exactly the stored scope, in this factory, under the
// policy in force now, so an approval of it is one Retry can use. Asking
// again while a request for this block is still open, or already approved,
// returns that request instead of filing another; a request the Owner
// answered otherwise is followed by a new one. Filing a request changes
// nothing about the issue: it stays Blocked until Retry succeeds.
//
// An issue intake did not block for want of an approval is NOT_FOUND, and a
// scope that now lies inside what agents may do on their own is CONFLICT:
// its retry needs no approval.
func (s *Service) RequestRetryApproval(ctx context.Context, issue core.IssueRef) (approvals.Request, error) {
	if err := issue.Validate(); err != nil {
		return approvals.Request{}, err
	}
	snap, err := s.load(ctx, issue)
	if err != nil {
		return approvals.Request{}, err
	}
	switch {
	case !snap.exists || snap.doc.Denial == nil || snap.record.State != core.StateBlocked:
		return approvals.Request{}, core.NewError(core.CodeNotFound, "intake did not block this issue for want of an approval")
	case snap.sow == nil:
		return approvals.Request{}, core.NewError(core.CodeInternal, "the issue was blocked for want of an approval but has no stored scope")
	}
	in, skipped, err := s.gather(ctx, issue, true)
	if err != nil {
		return approvals.Request{}, err
	}
	if skipped != "" {
		return approvals.Request{}, core.NewError(core.CodeConflict, "intake does not take this issue any more: "+skipped)
	}
	if err := in.policy.Revision.Validate(); err != nil {
		return approvals.Request{}, err
	}
	sow := *snap.sow
	verdict := Classify(draftOf(sow), in.issue.AuthorAssociation, in.policy.Catalog)
	if !verdict.NeedsApproval {
		return approvals.Request{}, core.NewError(core.CodeConflict, "the scope now lies inside the delegated scope; its retry needs no approval",
			core.NextAction{Action: "issues.retry", Description: "retry the issue"})
	}
	// The block is told apart by the record version it was written at, so
	// a later block of the same issue asks anew.
	base := fmt.Sprintf("intake:retry:%s:%d", requestSubject(sow, in.policy.Revision), snap.record.Version)
	for n := 1; n <= maxRetryRequests; n++ {
		id := base
		if n > 1 {
			id = fmt.Sprintf("%s:%d", base, n)
		}
		request, err := s.file(ctx, in, sow, verdict, id)
		if err != nil {
			return approvals.Request{}, err
		}
		if request.Status == approvals.StatusPending || request.Status == approvals.StatusApproved {
			return request, nil
		}
	}
	return approvals.Request{}, core.NewError(core.CodeLimitExceeded,
		fmt.Sprintf("the Owner was asked %d times about this block; the issue stays Blocked", maxRetryRequests))
}
