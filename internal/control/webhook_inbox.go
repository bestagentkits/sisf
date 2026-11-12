package control

import (
	"context"
	"encoding/json"
	"log/slog"

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
		payload, err := json.Marshal(ev)
		if err != nil {
			return core.NewError(core.CodeInternal, "the event could not be encoded").WithCause(err)
		}
		return store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			accepted, err := tx.AcceptDelivery(ctx, state.InboxEntry{
				Source: InboxSourceGitHub, DeliveryID: ev.DeliveryID, EventType: ev.Type,
				Payload: payload, ReceivedAt: ev.ReceivedAt,
			})
			if err != nil || !accepted {
				// A repeated delivery is already stored and projected.
				return err
			}
			return projectEvent(ctx, tx, ev)
		})
	}
}
