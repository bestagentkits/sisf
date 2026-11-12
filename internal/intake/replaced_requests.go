package intake

import (
	"context"
	"errors"
	"slices"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// replacedRequest is an approval request an issue stopped waiting on, and
// the request that replaced it.
type replacedRequest struct {
	RequestID string `json:"request_id"`
	By        string `json:"by"`
}

// owesClose returns closing with the request old added as replaced by
// replacement, unless there is nothing to close: no old request, or the
// same one.
func owesClose(closing []replacedRequest, old, replacement string) []replacedRequest {
	owed := slices.Clone(closing)
	if old == "" || old == replacement {
		return owed
	}
	return append(owed, replacedRequest{RequestID: old, By: replacement})
}

// closeReplaced closes the approval requests doc owes a close, so the Owner
// can no longer decide them and nothing can spend them, and removes from
// the stored document those it closed. It runs after the step that replaced
// them is written, and on every later pass until each close is made.
//
// A request that can never be closed, because it is unknown, belongs to
// another factory or is not replaced by a request about the same issue and
// action, is no longer owed. Any other failure keeps the close owed and is
// returned; the step that replaced the request stands either way.
func (s *Service) closeReplaced(ctx context.Context, doc issueDoc) error {
	if len(doc.Closing) == 0 {
		return nil
	}
	var (
		settled []replacedRequest
		failed  error
	)
	for _, owed := range doc.Closing {
		_, err := s.cfg.Approvals.Supersede(ctx, owed.RequestID, owed.By)
		switch {
		case err == nil, core.HasCode(err, core.CodeNotFound), core.HasCode(err, core.CodeInvalidArgument),
			core.HasCode(err, core.CodePermissionDenied):
			settled = append(settled, owed)
		default:
			failed = errors.Join(failed, err)
		}
	}
	if len(settled) > 0 {
		if err := s.settleCloses(ctx, doc.Issue, settled); err != nil {
			failed = errors.Join(failed, err)
		}
	}
	return failed
}

// settleCloses removes the closes that were made from the stored document of
// issue. A document another writer changed meanwhile is read again; a close
// left listed is made again on the next pass, which is harmless.
func (s *Service) settleCloses(ctx context.Context, issue core.IssueRef, settled []replacedRequest) error {
	return s.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, namespace, issueKey(issue))
		if err != nil {
			return err
		}
		var doc issueDoc
		if err := decode(entry, &doc); err != nil {
			return err
		}
		remaining := slices.DeleteFunc(slices.Clone(doc.Closing), func(owed replacedRequest) bool {
			return slices.Contains(settled, owed)
		})
		if len(remaining) == len(doc.Closing) {
			return nil
		}
		doc.Closing = remaining
		return put(ctx, tx, issueKey(issue), entry.Version, doc)
	})
}
