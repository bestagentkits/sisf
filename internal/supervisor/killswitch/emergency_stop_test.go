package killswitch

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// The switch is the release pipeline's freeze check, and it holds the lease
// the dispatcher leads under.
var _ release.FreezeCheck = (*Switch)(nil)

func TestTheSwitchHoldsTheDispatcherLeadershipLease(t *testing.T) {
	t.Parallel()
	if leadershipResource != dispatcher.LeaseResource {
		t.Fatalf("the switch holds %q, the dispatcher leads under %q", leadershipResource, dispatcher.LeaseResource)
	}
}

func stopRequest() supervisor.EmergencyStopRequest {
	return supervisor.EmergencyStopRequest{Actor: "user:owner", Reason: "runaway spend"}
}

func TestAnEmergencyStopHaltsClaimsRunsSecretsAndPublishing(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	f.ready(1)
	f.developing(2, "run-2", time.Hour)
	s := f.kill()
	ctx := context.Background()

	report, err := s.Stop(ctx, stopRequest())
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if report.StopID == "" || !report.PublishingFrozen || len(report.Failures) != 0 {
		t.Fatalf("report %+v", report)
	}
	if frozen, err := s.Frozen(ctx); !frozen || err != nil {
		t.Fatalf("Frozen = %v, %v, want frozen", frozen, err)
	}
	// The Ready issue cannot be claimed; the run in flight keeps its lease
	// but is cancelled and loses its secrets.
	if !slices.Equal(report.ClaimsHeld, []core.IssueRef{issue(1)}) {
		t.Fatalf("claims held %v, want the Ready issue", report.ClaimsHeld)
	}
	if _, err := f.claim(1, "dispatcher-1", time.Hour); !core.HasCode(err, core.CodeLeaseHeld) {
		t.Fatalf("a claim during the stop = %v, want LEASE_HELD", err)
	}
	if len(f.runtime.cancelled) != 1 || f.runtime.cancelled[0].ID != "agent-run-2" ||
		!slices.Equal(f.runtime.revoked, []core.RunID{"run-2"}) ||
		!slices.Equal(report.RunsCancelled, []core.RunID{"run-2"}) || !slices.Equal(report.SecretsRevoked, []core.RunID{"run-2"}) {
		t.Fatalf("cancelled %v revoked %v report %+v", f.runtime.cancelled, f.runtime.revoked, report)
	}
	if err := f.agent.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		_, err := tx.AcquireLease(ctx, dispatcher.LeaseResource, "dispatcher-2", time.Minute)
		return err
	}); !core.HasCode(err, core.CodeLeaseHeld) {
		t.Fatalf("a dispatcher taking the lead during the stop = %v, want LEASE_HELD", err)
	}
	status, err := s.Status(ctx)
	if err != nil || !status.Frozen || status.StopID != report.StopID || status.Reason != "runaway spend" {
		t.Fatalf("Status = %+v, %v", status, err)
	}
	if err := f.audit.Verify(ctx); err != nil {
		t.Fatalf("audit log: %v", err)
	}
}

func TestAStopIsIdempotentAndEnforcedOnIssuesThatArriveLater(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	s := f.kill()
	ctx := context.Background()
	first, err := s.Stop(ctx, stopRequest())
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	f.ready(3)
	second, err := s.Stop(ctx, supervisor.EmergencyStopRequest{Actor: "user:maintainer", Reason: "again"})
	if err != nil || second.StopID != first.StopID {
		t.Fatalf("second Stop = %+v, %v, want the first stop kept", second, err)
	}
	f.ready(4)
	report, err := s.Enforce(ctx)
	if err != nil || !slices.Contains(report.ClaimsHeld, issue(4)) {
		t.Fatalf("Enforce = %+v, %v, want the new issue held", report, err)
	}
	// The hold outlives one hold period because enforcement renews it.
	f.clock.Advance(DefaultHoldTTL - time.Minute)
	if _, err := s.Enforce(ctx); err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	f.clock.Advance(2 * time.Minute)
	if _, err := f.claim(4, "dispatcher-1", time.Hour); !core.HasCode(err, core.CodeLeaseHeld) {
		t.Fatalf("a claim after a renewed hold = %v, want LEASE_HELD", err)
	}
}

func TestWithoutAStopEnforceDoesNothing(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	f.ready(5)
	report, err := f.kill().Enforce(context.Background())
	if err != nil || report.PublishingFrozen || len(report.ClaimsHeld) != 0 {
		t.Fatalf("Enforce = %+v, %v", report, err)
	}
	if _, err := f.claim(5, "dispatcher-1", time.Hour); err != nil {
		t.Fatalf("a claim without a stop = %v", err)
	}
}

func TestARunThatLostItsLeaseIsFencedOffByTheStop(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	stale := f.developing(6, "run-6", time.Minute)
	f.clock.Advance(2 * time.Minute)
	report, err := f.kill().Stop(context.Background(), stopRequest())
	if err != nil || !slices.Contains(report.ClaimsHeld, issue(6)) {
		t.Fatalf("Stop = %+v, %v, want the abandoned issue held", report, err)
	}
	err = f.agent.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		_, err := tx.RenewLease(ctx, stale, time.Hour)
		return err
	})
	if !core.HasCode(err, core.CodeStaleFencingToken) {
		t.Fatalf("the old run renewing its lease = %v, want STALE_FENCING_TOKEN", err)
	}
}

func TestAPartThatFailsIsReportedAndTheRestOfTheStopHolds(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	f.developing(7, "run-7", time.Hour)
	f.runtime.cancelErr = core.NewError(core.CodeUnavailable, "the agent runtime is down")
	report, err := f.kill().Stop(context.Background(), stopRequest())
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(report.Failures) != 1 || len(report.RunsCancelled) != 0 || !slices.Equal(report.SecretsRevoked, []core.RunID{"run-7"}) {
		t.Fatalf("report %+v, want the cancel reported and the secrets still revoked", report)
	}

	bare := newFactory(t)
	bare.developing(8, "run-8", time.Hour)
	s, err := New(Config{FactoryID: testFactory, State: bare.control, Clock: bare.clock})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err = s.Stop(context.Background(), stopRequest())
	if err != nil || len(report.Failures) != 2 || !report.PublishingFrozen {
		t.Fatalf("Stop without a runtime = %+v, %v, want two failures and publishing frozen", report, err)
	}
}

func TestAStopThatCannotBeRecordedFailsAndAnUnreadableStopCountsAsFrozen(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	if err := f.control.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		_, err := tx.PutKV(ctx, state.KVEntry{Namespace: Namespace, Key: stopKey, Value: []byte("{not json")})
		return err
	}); err != nil {
		t.Fatalf("corrupt the stop: %v", err)
	}
	s := f.kill()
	if frozen, err := s.Frozen(context.Background()); !frozen || !core.HasCode(err, core.CodeTampered) {
		t.Fatalf("Frozen with an unreadable stop = %v, %v, want frozen and TAMPERED", frozen, err)
	}
	if report, err := s.Stop(context.Background(), stopRequest()); err != nil || report.StopID == "" {
		t.Fatalf("a stop over an unreadable record = %+v, %v, want a new stop", report, err)
	}

	closed := newFactory(t)
	_ = closed.control.Close()
	if _, err := closed.kill().Stop(context.Background(), stopRequest()); !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("a stop with the store gone = %v, want UNAVAILABLE", err)
	}
	if frozen, err := closed.kill().Frozen(context.Background()); !frozen || err == nil {
		t.Fatalf("Frozen with the store gone = %v, %v, want frozen with an error", frozen, err)
	}
}

func TestLiftingAStopLetsIssuesBeClaimedAndUnfreezesPublishing(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	f.ready(9)
	s := f.kill()
	ctx := context.Background()
	if err := s.Lift(ctx, "user:owner", "resolved"); !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("Lift without a stop = %v, want NOT_FOUND", err)
	}
	if _, err := s.Stop(ctx, stopRequest()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := s.Lift(ctx, " ", ""); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("Lift without an actor = %v, want INVALID_ARGUMENT", err)
	}
	if err := s.Lift(ctx, "user:owner", "the spend is under control"); err != nil {
		t.Fatalf("Lift: %v", err)
	}
	if frozen, err := s.Frozen(ctx); frozen || err != nil {
		t.Fatalf("Frozen after lifting = %v, %v", frozen, err)
	}
	if _, err := f.claim(9, "dispatcher-1", time.Hour); err != nil {
		t.Fatalf("a claim after lifting = %v", err)
	}
}

func TestTheSwitchAndItsRequestsAreValidated(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	for name, cfg := range map[string]Config{
		"no factory": {State: f.control, Clock: f.clock},
		"no store":   {FactoryID: testFactory, Clock: f.clock},
		"no clock":   {FactoryID: testFactory, State: f.control},
	} {
		if _, err := New(cfg); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: New = %v", name, err)
		}
	}
	if _, err := f.kill().Stop(context.Background(), supervisor.EmergencyStopRequest{Actor: "user:owner"}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("a stop without a reason = %v", err)
	}
	if frozen, _ := f.kill().Frozen(context.Background()); frozen {
		t.Error("a refused stop froze the factory")
	}
}
