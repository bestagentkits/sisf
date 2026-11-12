package wiring_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// While an unanswered issue cannot be parked because GitHub cannot be
// polled for answers, the factory's status says since when and why. Once a
// poll gets through, the issue is parked and the status says so no more.
func TestTheStatusShowsSinceWhenAndWhyParkingWaits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{OperatorToken: true})
	number := askedIssue(t, f)
	sync, err := f.Control.GitHubSync(ctx)
	if err != nil || !sync.ParkingDeferredSince.IsZero() || sync.ParkingDeferredReason != "" {
		t.Fatalf("before any deadline the status shows a deferral: %+v, %v", sync, err)
	}

	f.Clock.Advance(clarifyTimeout + time.Minute)
	f.GitHub.FailNext(1000, http.StatusBadGateway)
	_ = f.Control.Pass(ctx)
	if got := countComments(t, f, number, "Parked"); got != 0 {
		t.Fatal("the issue was parked although GitHub could not be polled for its answer")
	}
	sync, err = f.Control.GitHubSync(ctx)
	if err != nil || sync.ParkingDeferredSince.IsZero() || sync.ParkingDeferredSince.After(f.Clock.Now()) ||
		sync.ParkingDeferredReason == "" {
		t.Fatalf("the status does not show that parking waits: %+v, %v", sync, err)
	}
	// Further polls that fail store their own state; the deferral stays shown.
	for range 2 {
		f.Clock.Advance(wiring.DefaultReconcileInterval)
		_ = f.Control.Pass(ctx)
	}
	if again, err := f.Control.GitHubSync(ctx); err != nil || !again.ParkingDeferredSince.Equal(sync.ParkingDeferredSince) ||
		again.ParkingDeferredReason != sync.ParkingDeferredReason || again.LastError == "" {
		t.Fatalf("after more failed polls the status shows %+v, %v; want the deferral %+v", again, err, sync)
	}

	f.GitHub.ClearFaults()
	for range 3 {
		f.Clock.Advance(time.Minute)
		f.Pass()
	}
	if got := countComments(t, f, number, "Parked"); got != 1 {
		t.Fatalf("the issue was parked %d times once GitHub answered", got)
	}
	if sync, err = f.Control.GitHubSync(ctx); err != nil || !sync.ParkingDeferredSince.IsZero() || sync.ParkingDeferredReason != "" {
		t.Fatalf("the status still shows a deferral after the parking: %+v, %v", sync, err)
	}
}

// A deferral the status shows, as an earlier process of the factory left it
// stored, is taken back by the next pass once no parking waits.
func TestADeferralStoredByAnEarlierProcessIsTakenBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{OperatorToken: true})
	f.OpenIssue("maintainer", "MEMBER", "Health check fails", "The health endpoint is broken.", github.LabelReady, github.LabelKindBug)
	f.Pass()
	key := "github/" + f.Manifest.Repository.Owner + "/" + f.Manifest.Repository.Repo
	err := f.ControlStore.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, "control.reconcile", key)
		if err != nil {
			return err
		}
		var doc map[string]any
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			return err
		}
		doc["parking_deferred_since"], doc["parking_deferred_reason"] = f.Clock.Now().Add(-time.Hour), "left by an earlier process"
		if entry.Value, err = json.Marshal(doc); err != nil {
			return err
		}
		_, err = tx.PutKV(ctx, entry)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if sync, err := f.Control.GitHubSync(ctx); err != nil || sync.ParkingDeferredReason == "" {
		t.Fatalf("the stored deferral is not shown: %+v, %v", sync, err)
	}
	f.Pass()
	if sync, err := f.Control.GitHubSync(ctx); err != nil || !sync.ParkingDeferredSince.IsZero() || sync.ParkingDeferredReason != "" {
		t.Fatalf("a deferral no parking waits on is still shown: %+v, %v", sync, err)
	}
}
