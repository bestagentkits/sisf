package killswitch

import (
	"context"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Lift ends the stop in force: the stop document is removed, which unfreezes
// publishing, and the issues the switch holds are let go so they can be
// claimed again. Cancelled runs stay cancelled and revoked secrets stay
// revoked. Lifting needs the store itself, not the supervisor socket, which
// can only stop. It returns NOT_FOUND when no stop is in force.
func (s *Switch) Lift(ctx context.Context, actor, reason string) error {
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(reason) == "" {
		return core.NewError(core.CodeInvalidArgument, "lifting an emergency stop needs the actor who asks and a reason")
	}
	stop, version, found, err := s.current(ctx)
	if err != nil {
		return err
	}
	if !found {
		return core.NewError(core.CodeNotFound, "no emergency stop is in force")
	}
	// The issues are let go first: should that fail halfway, the stop is
	// still in force and the next pass of Enforce takes them back.
	if err := s.letGoAll(ctx); err != nil {
		return err
	}
	if err := s.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		return tx.DeleteKV(ctx, Namespace, stopKey, version)
	}); err != nil {
		return err
	}
	s.audit(ctx, LiftAction, core.OutcomeExecuted, map[string]string{"stop_id": stop.ID, "requested_by": actor, "reason": reason})
	return nil
}

// letGoAll releases every lease the switch holds. A lease is identified by
// its token, which only its holder can obtain again, so each is taken once
// more as the switch and released; a lease someone else holds is left alone.
func (s *Switch) letGoAll(ctx context.Context) error {
	records, _, err := s.survey(ctx)
	if err != nil {
		return err
	}
	resources := []string{leadershipResource}
	for _, r := range records {
		resources = append(resources, state.IssueLeaseResource(r.IssueID))
	}
	for _, resource := range resources {
		err := s.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			lease, err := tx.AcquireLease(ctx, resource, Holder, s.cfg.HoldTTL)
			if err != nil {
				return err
			}
			return tx.ReleaseLease(ctx, lease)
		})
		if err != nil && !core.HasCode(err, core.CodeLeaseHeld) {
			return err
		}
	}
	return nil
}
