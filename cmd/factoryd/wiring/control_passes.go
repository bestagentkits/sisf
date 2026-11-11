package wiring

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// Bounds of one pass, and of the waits after a failure.
const (
	inboxBatch   = 100
	outboxBatch  = 100
	firstBackoff = 2 * time.Second
	maxBackoff   = 5 * time.Minute
	// maxDeliveryAge is how long after its arrival a delivery that keeps
	// failing is tried again, at most every maxBackoff, before it is parked.
	// A day covers an outage of GitHub or a policy hold of a working day.
	maxDeliveryAge = 24 * time.Hour
)

// Who parks a delivery in the audit log, and the audited action.
const (
	inboxActor         = "service:control-inbox"
	actionParkDelivery = "inbox.park_delivery"
)

// backoffAfter is the wait after the given number of failures: it doubles
// from firstBackoff up to maxBackoff.
func backoffAfter(failures int) time.Duration {
	wait := firstBackoff
	for i := 1; i < failures && wait < maxBackoff; i++ {
		wait *= 2
	}
	return min(wait, maxBackoff)
}

// drainInbox hands every stored GitHub delivery that is due to intake,
// oldest first, and records how each one left the inbox. Intake decides
// from the issue as GitHub shows it now, so a delivery that arrives twice or
// out of order changes nothing; the inbox already keeps one entry per
// delivery ID.
//
// A delivery that cannot be read or is refused as malformed is finished as
// refused, and one whose issue or repository GitHub no longer knows is
// finished as gone: trying either again cannot help. Any other failure, such
// as GitHub being unreachable or the policy being on hold, holds the delivery
// back in the store for a growing wait, so the next pass asks only for
// deliveries that are due and a batch of failing ones cannot hold back those
// behind them. A delivery that still fails maxDeliveryAge after it arrived
// is parked: it is not tried again, and its last failure is kept in the
// inbox and the audit log for the Owner. It returns an error when the store
// failed or a delivery was held back or parked, and nil otherwise, even
// when deliveries beyond one batch are left for the next pass.
func (c *Control) drainInbox(ctx context.Context) error {
	var pending []state.InboxEntry
	err := c.cfg.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		pending, err = tx.PendingDeliveries(ctx, inboxBatch)
		return err
	})
	if err != nil {
		return err
	}
	var failed error
	for _, entry := range pending {
		if entry.Source != control.InboxSourceGitHub {
			continue
		}
		var ev github.WebhookEvent
		if err := json.Unmarshal(entry.Payload, &ev); err != nil {
			c.log.Warn("dropped a stored delivery that cannot be read", "delivery", entry.DeliveryID)
			err = c.cfg.Store.FinishDelivery(ctx, entry.Source, entry.DeliveryID, sqlstore.DeliveryRefused, "the delivery cannot be read")
			if err != nil {
				return err
			}
			continue
		}
		_, handleErr := c.Intake.HandleEvent(ctx, ev)
		if err := c.settle(ctx, entry, handleErr); err != nil {
			return err
		}
		if handleErr != nil && !permanent(handleErr) && !core.HasCode(handleErr, core.CodeNotFound) {
			failed = handleErr
		}
	}
	return failed
}

// settle records how intake's handling of a delivery ended: handled, refused,
// gone, held back for another try, or parked once it failed for too long.
func (c *Control) settle(ctx context.Context, entry state.InboxEntry, handleErr error) error {
	store := c.cfg.Store
	switch {
	case handleErr == nil:
		return store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			return tx.MarkDeliveryProcessed(ctx, entry.Source, entry.DeliveryID)
		})
	case core.HasCode(handleErr, core.CodeNotFound):
		c.log.Info("finished a delivery about an issue that is gone", "delivery", entry.DeliveryID)
		return store.FinishDelivery(ctx, entry.Source, entry.DeliveryID, sqlstore.DeliveryGone, reasonOf(handleErr))
	case permanent(handleErr):
		c.log.Warn("dropped a delivery intake refused", "delivery", entry.DeliveryID, "code", string(core.CodeOf(handleErr)))
		return store.FinishDelivery(ctx, entry.Source, entry.DeliveryID, sqlstore.DeliveryRefused, reasonOf(handleErr))
	}
	reason := reasonOf(handleErr)
	if c.cfg.Clock.Now().Sub(entry.ReceivedAt) < maxDeliveryAge {
		_, err := store.DeferDelivery(ctx, entry.Source, entry.DeliveryID, reason, backoffAfter)
		return err
	}
	if err := store.FinishDelivery(ctx, entry.Source, entry.DeliveryID, sqlstore.DeliveryParked, reason); err != nil {
		return err
	}
	c.log.Warn("parked a delivery that kept failing", "delivery", entry.DeliveryID, "code", string(core.CodeOf(handleErr)))
	_, err := c.Audit.Append(ctx, core.AuditEvent{
		At: c.cfg.Clock.Now(), FactoryID: c.cfg.Manifest.FactoryID, ActorID: inboxActor, ActorKind: core.ActorService,
		Action: actionParkDelivery, Resource: "delivery:" + entry.Source + "/" + entry.DeliveryID, Outcome: core.OutcomeFailed,
		Details: map[string]string{"code": string(core.CodeOf(handleErr)), "reason": reason, "received_at": entry.ReceivedAt.UTC().Format(time.RFC3339)},
	})
	return err
}

// permanent reports whether retrying the same input can never succeed.
func permanent(err error) bool {
	switch core.CodeOf(err) {
	case core.CodeInvalidArgument, core.CodeNotSupported, core.CodePermissionDenied:
		return true
	}
	return false
}

// due reports whether a delivery may be tried at now.
func (c *Control) due(id string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	next, waiting := c.backoff[id]
	return !waiting || !now.Before(next.at)
}

// failed schedules the next try of a delivery.
func (c *Control) failed(id string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := c.backoff[id]
	next.failures++
	next.at = now.Add(backoffAfter(next.failures))
	c.backoff[id] = next
}

// deliverOutbox performs the effects intake queued, oldest first. A comment
// carries a hidden marker, so an effect performed twice shows once; a failed
// one is tried again after a growing wait. Messages of other topics belong
// to other components and are left alone. The supervisor's recovery
// reports, held build notices and QA hold notices are delivered here too,
// as issue comments.
// Each is recorded as the factory's own edit of its issue (editIssue).
func (c *Control) deliverOutbox(ctx context.Context) error {
	var pending []state.OutboxMessage
	err := c.cfg.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		pending, err = tx.PendingOutbox(ctx, outboxBatch)
		return err
	})
	if err != nil {
		return err
	}
	var failed error
	for _, m := range pending {
		var deliver func(ctx context.Context) error
		sent := func(ctx context.Context, tx state.Tx) error { return tx.MarkOutboxSent(ctx, m.ID) }
		switch m.Topic {
		case intake.TopicComment, intake.TopicLabels:
			deliver = func(ctx context.Context) error { return intake.Deliver(ctx, c.cfg.GitHub, m) }
			sent = func(ctx context.Context, tx state.Tx) error { return intake.Delivered(ctx, tx, m) }
		case release.TopicRecoveryReport:
			deliver = func(ctx context.Context) error { return c.deliverRecoveryReport(ctx, m) }
		case handoff.TopicBuildHeld:
			deliver = func(ctx context.Context) error { return c.deliverBuildHeld(ctx, m) }
		case handoff.TopicQAHeld:
			deliver = func(ctx context.Context) error { return c.deliverQAHeld(ctx, m) }
		default:
			continue
		}
		sendErr := c.editIssue(ctx, outboxIssue(m), outboxIntent(m), deliver)
		now := c.cfg.Clock.Now()
		err := c.cfg.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			if sendErr == nil {
				return sent(ctx, tx)
			}
			return tx.MarkOutboxFailed(ctx, m.ID, now.Add(backoffAfter(m.Attempts+1)), reasonOf(sendErr))
		})
		if err != nil {
			return err
		}
		if sendErr != nil {
			failed = sendErr
		}
	}
	return failed
}

// reasonOf is a short, typed description of a failure for the outbox. A
// typed error's message is written for callers; anything else is replaced.
func reasonOf(err error) string {
	code := core.CodeOf(err)
	var typed *core.Error
	if !errors.As(err, &typed) {
		return string(code)
	}
	return strings.TrimSpace(string(code) + ": " + typed.Message)
}
