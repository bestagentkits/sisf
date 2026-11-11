package control

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// InboxSourceGitHub is the inbox source of deliveries stored by the webhook
// route. The consumer that drains the inbox filters on it.
const InboxSourceGitHub = "github"

// newInboxSink returns the sink the GitHub receiver hands verified events to.
//
// It stores the delivery in the inbox and returns; nothing is processed here.
// The inbox keeps one entry per delivery ID in the database itself, so a
// retry, a redelivery or a restart of this process cannot create a second
// entry. The receiver answers 2xx only after the sink returned nil, so GitHub
// retries a delivery that was not stored.
//
// An echo of the factory's own edit is acknowledged and dropped: it carries
// no new input, and storing it would let the factory's label or comment
// writes feed the factory again.
func newInboxSink(store state.Store, log *slog.Logger) github.Sink {
	return func(ctx context.Context, ev github.WebhookEvent) error {
		if ev.FromSelf {
			log.Debug("dropped an echo of the factory's own edit", "delivery", ev.DeliveryID)
			return nil
		}
		return store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			_, err := acceptEvent(ctx, tx, ev)
			return err
		})
	}
}

// acceptEvent stores ev in the inbox and folds it into its issue's view,
// unless an entry with its delivery ID is already there.
func acceptEvent(ctx context.Context, tx state.Tx, ev github.WebhookEvent) (bool, error) {
	payload, err := json.Marshal(ev)
	if err != nil {
		return false, core.NewError(core.CodeInternal, "the event could not be encoded").WithCause(err)
	}
	accepted, err := tx.AcceptDelivery(ctx, state.InboxEntry{
		Source: InboxSourceGitHub, DeliveryID: ev.DeliveryID, EventType: ev.Type,
		Payload: payload, ReceivedAt: ev.ReceivedAt,
	})
	if err != nil || !accepted {
		// A repeated delivery is already stored and projected.
		return false, err
	}
	return true, projectEvent(ctx, tx, ev)
}

// AcceptReconciledIssue stores a change that reconciliation found on GitHub
// as an inbox entry, the way the webhook route stores a delivery, so the
// same drain hands it to intake. ev carries the issue as GitHub lists it now
// and a delivery ID derived from the issue and its update time, so the same
// change found twice is stored once.
//
// A change the inbox already holds is not stored again: when a webhook
// about the issue at this update time, or a later one, was stored, the
// issue's view records that time and nothing was missed. Nor is the
// factory's own edit (see RecordFactoryWrite): the issue's view takes it in
// without an inbox entry, so intake is not run for what it wrote itself. It
// reports whether the entry was stored.
func AcceptReconciledIssue(ctx context.Context, store state.Store, ev github.WebhookEvent) (bool, error) {
	if ev.Issue == nil || ev.Issue.UpdatedAt.IsZero() || ev.FromSelf {
		return false, core.NewError(core.CodeInvalidArgument, "a reconciled change names an issue and its update time")
	}
	if err := ev.Validate(); err != nil {
		return false, err
	}
	var accepted bool
	err := store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		seen, _, err := readProjection(ctx, tx, ev.Issue.Ref)
		if err != nil {
			return err
		}
		switch {
		case !ev.Issue.UpdatedAt.After(seen.IssueAt):
			accepted = false
			return nil
		case ev.Issue.UpdatedAt.Equal(seen.SelfAt):
			accepted = false
			return projectEvent(ctx, tx, ev)
		}
		accepted, err = acceptEvent(ctx, tx, ev)
		return err
	})
	return accepted, err
}

// RecordFactoryWrite notes that the factory edited an issue: before is the
// issue's update time GitHub showed just before the edit, after the one it
// showed just after. The edit is recognized by a later poll only when
// before is a change already taken in (the issue's view holds it, or it was
// the factory's own previous edit): a person's change whose webhook was
// lost before the edit is then still found, because the issue no longer
// stands at an update time the factory caused alone. An issue with no view
// yet has nothing taken in, and nothing is recorded.
//
// A person's change in the moment between the two reads, with its webhook
// lost too, is taken in with the issue's next change instead.
func RecordFactoryWrite(ctx context.Context, store state.Store, ref core.IssueRef, before, after time.Time) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if before.IsZero() || after.IsZero() || after.Before(before) {
		return core.NewError(core.CodeInvalidArgument, "a factory edit needs the issue's update times before and after it, in order")
	}
	return store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		current, version, err := readProjection(ctx, tx, ref)
		if err != nil || version == 0 {
			return err
		}
		next := current
		next.SelfAt = time.Time{}
		if !before.After(current.IssueAt) || before.Equal(current.SelfAt) {
			next.SelfAt = after
		}
		if next.SelfAt.Equal(current.SelfAt) {
			return nil
		}
		value, err := json.Marshal(next)
		if err != nil {
			return core.NewError(core.CodeInternal, "the issue view could not be encoded").WithCause(err)
		}
		_, err = tx.PutKV(ctx, state.KVEntry{Namespace: issueViewNamespace, Key: ref.String(), Value: value, Version: version})
		return err
	})
}
