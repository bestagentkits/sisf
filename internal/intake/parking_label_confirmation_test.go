package intake_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// parkedUndelivered parks the asked issue on the timer, which queues its
// sisf:needs-info label without delivering it.
func parkedUndelivered(t *testing.T) *factory {
	t.Helper()
	f := asked(t)
	f.clock.Advance(clarifyTimeout)
	if parked, err := f.service.ExpireClarifications(context.Background(), f.clock.Now()); err != nil || len(parked) != 1 {
		t.Fatalf("ExpireClarifications = %v, %v", parked, err)
	}
	if f.hasLabel(github.LabelNeedsInfo) {
		t.Fatal("the parking label reached GitHub before it was delivered")
	}
	return f
}

// storedBeforeConfirmation rewrites the issue's intake document as a
// release that did not yet record whether a parking's label reached GitHub
// stored it: without either mark.
func (f *factory) storedBeforeConfirmation() {
	f.t.Helper()
	err := f.state.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, "intake", "issue/"+issueRef.String())
		if err != nil {
			return err
		}
		var doc map[string]any
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			return err
		}
		clarify, ok := doc["clarify"].(map[string]any)
		if !ok || clarify["needs_info"] != true {
			f.t.Fatalf("the stored document is not parked: %s", entry.Value)
		}
		delete(clarify, "label_pending")
		delete(clarify, "label_confirmed")
		raw, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		entry.Value = raw
		_, err = tx.PutKV(ctx, entry)
		return err
	})
	if err != nil {
		f.t.Fatalf("store the document as an earlier release did: %v", err)
	}
}

// wantStillParked fails unless the issue is parked as it was: in
// Clarifying, asked once and not asked again.
func (f *factory) wantStillParked() {
	f.t.Helper()
	f.wantState(core.StateClarifying)
	if n := f.count("More information needed"); n != 1 {
		f.t.Fatalf("a parking that was not lifted was asked again (%d questions): %q", n, f.github.factoryComments(issueRef))
	}
}

// A removal webhook that arrives late, from an earlier parking, while the
// label of the current one is still queued: the issue does not carry the
// label, but nobody lifted this parking.
func TestALateRemovalWebhookDoesNotLiftAParkingWhoseLabelIsStillQueued(t *testing.T) {
	t.Parallel()
	f := parkedUndelivered(t)
	stale := f.event("issues", "unlabeled", func(ev *github.WebhookEvent) { ev.Label = &github.Label{Name: github.LabelNeedsInfo} })
	before := f.spent()
	result, err := f.service.HandleEvent(context.Background(), stale)
	if err != nil || result.Skipped == "" || len(result.Events) != 0 {
		t.Fatalf("a late removal webhook moved the parked issue: %+v, %v", result, err)
	}
	if after := f.spent(); after.effects != before.effects || after.github != before.github {
		t.Fatalf("the late webhook read or queued something: before %+v, after %+v", before, after)
	}
	f.deliver()
	if !f.hasLabel(github.LabelNeedsInfo) {
		t.Fatal("the parking label was not delivered")
	}
	f.wantStillParked()

	// Once the label is on GitHub, a person's removal lifts the parking.
	f.github.unlabel(issueRef, github.LabelNeedsInfo)
	f.github.comment(issueRef, member, criteria)
	wantEvents(t, f.handle(f.event("issues", "unlabeled", func(ev *github.WebhookEvent) {
		ev.Label = &github.Label{Name: github.LabelNeedsInfo}
	})), core.EventClarificationCompleted, core.EventScopePermitted)
}

// A parking stored before the factory recorded whether its label reached
// GitHub, whose label was still queued at the upgrade: it is not lifted by
// events that show the issue without the label, and it is confirmed when
// the queued label is delivered.
func TestAParkingStoredBeforeUpgradeWithItsLabelQueuedIsNotLifted(t *testing.T) {
	t.Parallel()
	f := parkedUndelivered(t)
	f.storedBeforeConfirmation()
	for _, ev := range []github.WebhookEvent{
		f.event("issues", "reconciled", nil),
		f.event("issue_comment", "created", nil),
		f.event("issues", "unlabeled", func(ev *github.WebhookEvent) { ev.Label = &github.Label{Name: github.LabelNeedsInfo} }),
	} {
		result, err := f.service.HandleEvent(context.Background(), ev)
		if err != nil || result.Skipped == "" || len(result.Events) != 0 {
			t.Fatalf("event %s/%s moved a parking whose label was never delivered: %+v, %v", ev.Type, ev.Action, result, err)
		}
	}
	f.deliver()
	if !f.hasLabel(github.LabelNeedsInfo) {
		t.Fatal("the queued parking label was not delivered")
	}
	f.wantStillParked()

	f.github.unlabel(issueRef, github.LabelNeedsInfo)
	f.github.comment(issueRef, member, criteria)
	wantEvents(t, f.handle(f.event("issues", "reconciled", nil)), core.EventClarificationCompleted, core.EventScopePermitted)
}

// A parking stored before the upgrade whose label did reach GitHub is
// confirmed by the first event that shows the label, and a person's later
// removal lifts it.
func TestAParkingStoredBeforeUpgradeIsConfirmedWhenItsLabelIsSeen(t *testing.T) {
	t.Parallel()
	f := asked(t)
	f.clock.Advance(clarifyTimeout)
	f.reconcile()
	if !f.hasLabel(github.LabelNeedsInfo) {
		t.Fatal("the issue was not parked")
	}
	f.storedBeforeConfirmation()
	f.clock.Advance(time.Minute)
	if result := f.handle(f.event("issues", "edited", nil)); result.Skipped == "" || len(result.Events) != 0 {
		t.Fatalf("an event that shows the label moved the parked issue: %+v", result)
	}
	f.wantStillParked()

	f.github.unlabel(issueRef, github.LabelNeedsInfo)
	f.github.comment(issueRef, member, criteria)
	wantEvents(t, f.handle(f.event("issues", "unlabeled", func(ev *github.WebhookEvent) {
		ev.Label = &github.Label{Name: github.LabelNeedsInfo}
	})), core.EventClarificationCompleted, core.EventScopePermitted)
}
