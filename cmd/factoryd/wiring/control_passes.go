package wiring

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Bounds of one pass, and of the waits after a failure.
const (
	inboxBatch   = 100
	outboxBatch  = 100
	firstBackoff = 2 * time.Second
	maxBackoff   = 5 * time.Minute
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

// drainInbox hands every stored GitHub delivery to intake, oldest first, and
// marks it processed once intake is done with it. Intake decides from the
// issue as GitHub shows it now, so a delivery that arrives twice or out of
// order changes nothing; the inbox already keeps one entry per delivery ID.
//
// A delivery that cannot be decoded or is refused as malformed is marked
// processed: trying it again cannot help. Any other failure, such as GitHub
// being unreachable or the policy being on hold, leaves it in the inbox to
// be tried again after a growing wait.
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
	now := c.cfg.Clock.Now()
	for _, entry := range pending {
		if entry.Source != control.InboxSourceGitHub || !c.due(entry.DeliveryID, now) {
			continue
		}
		var ev github.WebhookEvent
		if err := json.Unmarshal(entry.Payload, &ev); err != nil {
			c.log.Warn("dropped a stored delivery that cannot be read", "delivery", entry.DeliveryID)
		} else if _, err := c.Intake.HandleEvent(ctx, ev); err != nil && !permanent(err) {
			c.failed(entry.DeliveryID, now)
			failed = err
			continue
		} else if err != nil {
			c.log.Warn("dropped a delivery intake refused", "delivery", entry.DeliveryID, "code", string(core.CodeOf(err)))
		}
		if err := c.cfg.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			return tx.MarkDeliveryProcessed(ctx, entry.Source, entry.DeliveryID)
		}); err != nil {
			return err
		}
		c.mu.Lock()
		delete(c.backoff, entry.DeliveryID)
		c.mu.Unlock()
	}
	return failed
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
// to other components and are left alone.
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
		if m.Topic != intake.TopicComment && m.Topic != intake.TopicLabels {
			continue
		}
		sendErr := intake.Deliver(ctx, c.cfg.GitHub, m)
		now := c.cfg.Clock.Now()
		err := c.cfg.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			if sendErr == nil {
				return tx.MarkOutboxSent(ctx, m.ID)
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
