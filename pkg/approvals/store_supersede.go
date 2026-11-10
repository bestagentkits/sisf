package approvals

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// supersession records that a request was closed because another one
// replaced it, and which one.
type supersession struct {
	RequestID string    `json:"request_id"`
	By        string    `json:"by"`
	At        time.Time `json:"at"`
}

// Supersede closes a request that another request replaces, such as the
// approval request of a scope that was edited since it was filed.
//
// A request still open, or approved but not used, is closed: it is stored as
// expired, so it can no longer be decided (EXPIRED) or spent (EXPIRED), and a
// record names the request that replaced it. A request that was already
// rejected, answered with changes, lapsed or used stays as it is; it cannot
// be decided or spent either. Closing a request twice is harmless.
//
// replacement must name another request of this factory about the same
// issue and action, otherwise INVALID_ARGUMENT; an unknown request is
// NOT_FOUND. The closing is audited, and an audit failure is returned with
// the request already closed.
func (s *KVStore) Supersede(ctx context.Context, id, replacement string) (Request, error) {
	if replacement == "" || replacement == id {
		return Request{}, invalid("a request is superseded by another request")
	}
	var (
		closed  Request
		changed bool
	)
	err := s.update(ctx, func(ctx context.Context, tx state.Tx) error {
		changed = false
		r, version, err := loadRequest(ctx, tx, id)
		if err != nil {
			return err
		}
		next, _, err := loadRequest(ctx, tx, replacement)
		if err != nil {
			return err
		}
		switch {
		case r.FactoryID != s.factory || next.FactoryID != s.factory:
			return core.NewError(core.CodePermissionDenied, "the approval request belongs to another factory than this store serves")
		case next.IssueID != r.IssueID || next.Action != r.Action:
			return invalid("a request is superseded only by a request about the same issue and action")
		}
		closed = s.current(r)
		if closed.Status != StatusPending && closed.Status != StatusApproved {
			return nil
		}
		r.Status = StatusExpired
		if err := put(ctx, tx, supersededKey(r.ID), 0, supersession{RequestID: r.ID, By: replacement, At: s.clock.Now()}); err != nil {
			return err
		}
		if err := put(ctx, tx, requestKey(r.ID), version, r); err != nil {
			return err
		}
		closed, changed = r, true
		return nil
	})
	if err != nil {
		return Request{}, err
	}
	if !changed {
		return closed, nil
	}
	actor := s.service(closed.RequestedBy)
	actor.details["superseded_by"] = replacement
	if err := s.record(ctx, closed, actionSupersede, actor, nil); err != nil {
		return Request{}, err
	}
	return closed, nil
}

// SupersededBy returns the request that replaced id, or "" when id was not
// superseded.
func (s *KVStore) SupersededBy(ctx context.Context, id string) (string, error) {
	var by string
	err := s.state.View(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, namespace, supersededKey(id))
		switch {
		case core.HasCode(err, core.CodeNotFound):
			return nil
		case err != nil:
			return err
		}
		var record supersession
		if err := decode(entry, &record); err != nil {
			return err
		}
		by = record.By
		return nil
	})
	return by, err
}
