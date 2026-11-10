package wiring_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

const bugBody = "The health check fails when the database is slow.\n\n### Definition of Done\n- /healthz answers 200 within one second while the database is slow\n"

// storeDeliveries puts n deliveries about issue numbers from first on into
// the inbox, received a minute before now, as the webhook route would.
func storeDeliveries(t *testing.T, f *harness.Factory, prefix string, first, n int) {
	t.Helper()
	repo := github.RepoRef{Owner: f.Manifest.Repository.Owner, Name: f.Manifest.Repository.Repo}
	received := f.Clock.Now().Add(-time.Minute)
	err := f.ControlStore.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		for i := range n {
			id := fmt.Sprintf("%s-%03d", prefix, i)
			ev := github.WebhookEvent{DeliveryID: id, Type: "issues", Action: "labeled", InstallationID: 1, Repo: repo,
				Issue: &github.Issue{Ref: f.Issue(first + i)}, ReceivedAt: received}
			payload, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			if _, err := tx.AcceptDelivery(ctx, state.InboxEntry{Source: control.InboxSourceGitHub, DeliveryID: id,
				EventType: ev.Type, Payload: payload, ReceivedAt: received}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Deliveries that keep failing do not hold back the ones behind them, both
// when their issue is gone and when GitHub keeps refusing them for now: a
// delivery that arrives after a full batch of them is processed.
func TestFailingDeliveriesDoNotBlockTheInbox(t *testing.T) {
	t.Parallel()
	for name, poison := range map[string]func(f *harness.Factory){
		// Issue numbers nobody opened: GitHub answers 404.
		"issue gone": func(*harness.Factory) {},
		// GitHub answers every request about them with a conflict.
		"failing for now": func(f *harness.Factory) { f.GitHub.FailNext(150, http.StatusConflict) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := harness.New(t, harness.Options{})
			f.Pass() // the policy references are published before any fault
			storeDeliveries(t, f, "poisoned", 500, 150)
			poison(f)
			number := f.OpenIssue("maintainer", "MEMBER", "Health check fails", bugBody, github.LabelReady, github.LabelKindBug)
			for range 3 {
				_ = f.Control.Pass(context.Background())
			}
			if got := f.Record(number).State; got != core.StateReady {
				t.Fatalf("the delivery behind the failing ones was not processed: issue is %s", got)
			}
		})
	}
}

// A delivery that still fails a day after it arrived is parked: it is not
// tried again, and its last failure is kept in the inbox and in the audit
// log for the Owner. Until then it is held back between tries.
func TestADeliveryThatKeepsFailingIsParkedWithItsReason(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	f.Pass()
	storeDeliveries(t, f, "failing", 900, 1)
	const id = "failing-000"

	// GitHub refuses every request of the passes below.
	f.GitHub.FailNext(1000, http.StatusConflict)
	_ = f.Control.Pass(ctx)
	record, err := f.ControlStore.Delivery(ctx, control.InboxSourceGitHub, id)
	if err != nil || record.Attempts != 1 || record.Outcome != "" || record.LastError == "" || !record.NextAttemptAt.After(f.Clock.Now()) {
		t.Fatalf("the failed delivery was not held back: %+v, %v", record, err)
	}
	_ = f.Control.Pass(ctx) // not due yet
	if record, _ := f.ControlStore.Delivery(ctx, control.InboxSourceGitHub, id); record.Attempts != 1 {
		t.Fatalf("a delivery was tried before its time: %+v", record)
	}

	f.Clock.Advance(24 * time.Hour)
	_ = f.Control.Pass(ctx)
	parked, err := f.ControlStore.Delivery(ctx, control.InboxSourceGitHub, id)
	if err != nil || parked.Outcome != sqlstore.DeliveryParked || parked.ProcessedAt.IsZero() || parked.LastError == "" {
		t.Fatalf("the delivery was not parked with its reason: %+v, %v", parked, err)
	}
	events, err := f.Control.Audit.Events(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	audited := 0
	for _, e := range events {
		if e.Action == "inbox.park_delivery" && e.Resource == "delivery:github/"+id && e.Details["reason"] == parked.LastError {
			audited++
		}
	}
	if audited != 1 {
		t.Fatalf("%d audit records of the parking with its reason, want 1", audited)
	}
	f.Clock.Advance(time.Hour)
	_ = f.Control.Pass(ctx)
	if again, _ := f.ControlStore.Delivery(ctx, control.InboxSourceGitHub, id); again != parked {
		t.Fatalf("a parked delivery was tried again: %+v, was %+v", again, parked)
	}
}

// A delivery about an issue GitHub no longer knows leaves the inbox at once,
// as gone, with GitHub's answer kept.
func TestADeliveryAboutAnIssueThatIsGoneIsFinishedAtOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	storeDeliveries(t, f, "gone", 700, 1)
	if err := f.Control.Pass(ctx); err != nil {
		t.Fatalf("a delivery about a missing issue failed the pass: %v", err)
	}
	record, err := f.ControlStore.Delivery(ctx, control.InboxSourceGitHub, "gone-000")
	if err != nil || record.Outcome != sqlstore.DeliveryGone || record.ProcessedAt.IsZero() || !strings.Contains(record.LastError, "NOT_FOUND") {
		t.Fatalf("the delivery was not finished as gone: %+v, %v", record, err)
	}
}
