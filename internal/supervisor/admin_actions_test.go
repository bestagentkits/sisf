package supervisor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// serveActions serves the admin mux with opts on an ephemeral loopback port
// and returns a client of it.
func serveActions(t *testing.T, opts Options) (*Client, string) {
	t.Helper()
	server := httptest.NewServer(newAdminMux(func() Status { return Status{FactoryID: testFactory} }, opts))
	t.Cleanup(server.Close)
	addr := strings.TrimPrefix(server.URL, "http://")
	client, err := NewClient(addr)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client, server.URL
}

func TestTheAdminSocketCarriesTheEmergencyStopRollbackAndFreezeStatus(t *testing.T) {
	t.Parallel()
	var stopped EmergencyStopRequest
	var rolled RollbackRequest
	client, _ := serveActions(t, Options{
		EmergencyStop: func(_ context.Context, req EmergencyStopRequest) (EmergencyStopReport, error) {
			stopped = req
			return EmergencyStopReport{StopID: "stop-1", PublishingFrozen: true}, nil
		},
		FreezeStatus: func(context.Context) (FreezeStatus, error) { return FreezeStatus{Frozen: true, StopID: "stop-1"}, nil },
		Rollback: func(_ context.Context, req RollbackRequest) (RollbackReport, error) {
			rolled = req
			return RollbackReport{Unit: req.Unit, RestoredTo: core.ReleaseDigest(appDigest), Observed: core.ReleaseDigest(appDigest), Healthy: true}, nil
		},
	})
	ctx := context.Background()
	report, err := client.EmergencyStop(ctx, EmergencyStopRequest{FactoryID: testFactory, Actor: "user:owner", Reason: "runaway spend"})
	if err != nil || report.StopID != "stop-1" || stopped.Reason != "runaway spend" {
		t.Fatalf("EmergencyStop = %+v, %v (server saw %+v)", report, err, stopped)
	}
	status, err := client.FreezeStatus(ctx)
	if err != nil || !status.Frozen {
		t.Fatalf("FreezeStatus = %+v, %v", status, err)
	}
	back, err := client.RequestRollback(ctx, RollbackRequest{FactoryID: testFactory, Unit: "app", Reason: "broken", ApprovalRef: "apr-1"})
	if err != nil || back.Observed != core.ReleaseDigest(appDigest) || rolled.ApprovalRef != "apr-1" {
		t.Fatalf("RequestRollback = %+v, %v (server saw %+v)", back, err, rolled)
	}
}

func TestARefusalOnTheAdminSocketKeepsItsCode(t *testing.T) {
	t.Parallel()
	client, _ := serveActions(t, Options{
		Rollback: func(context.Context, RollbackRequest) (RollbackReport, error) {
			return RollbackReport{}, core.NewError(core.CodeReplayed, "the approval was already used")
		},
	})
	_, err := client.RequestRollback(context.Background(), RollbackRequest{FactoryID: testFactory, Unit: "app", Reason: "broken", ApprovalRef: "apr-1"})
	if !core.HasCode(err, core.CodeReplayed) {
		t.Fatalf("RequestRollback = %v, want REPLAYED", err)
	}
}

func TestARollbackWithoutAnApprovalNeverReachesTheSupervisor(t *testing.T) {
	t.Parallel()
	called := false
	client, url := serveActions(t, Options{Rollback: func(context.Context, RollbackRequest) (RollbackReport, error) {
		called = true
		return RollbackReport{}, nil
	}})
	_, err := client.RequestRollback(context.Background(), RollbackRequest{FactoryID: testFactory, Unit: "app", Reason: "broken"})
	if !core.HasCode(err, core.CodeApprovalRequired) {
		t.Fatalf("client without approval = %v, want APPROVAL_REQUIRED", err)
	}
	// A client that skips the check is refused by the socket itself.
	resp, err := http.Post(url+RollbackPath, "application/json", strings.NewReader(`{"factory_id":"acme-hello","unit":"app","reason":"x"}`))
	if err != nil {
		t.Fatalf("raw post: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK || called {
		t.Fatalf("a rollback without an approval reached the supervisor (status %d)", resp.StatusCode)
	}
}

func TestActionsAreNotServedUnlessTheSupervisorWasGivenThem(t *testing.T) {
	t.Parallel()
	_, url := serveActions(t, Options{})
	for _, path := range []string{EmergencyStopPath, RollbackPath} {
		resp, err := http.Post(url+path, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("post %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST %s answered %d on a supervisor without actions", path, resp.StatusCode)
		}
	}
}

// Two factories on one host share the default admin address, so a stop
// must name the factory it stops and a supervisor of another factory, or a
// stop that names none, never carries it out.
func TestAnEmergencyStopForAnotherFactoryIsRefusedAndNeverRuns(t *testing.T) {
	t.Parallel()
	called := false
	client, _ := serveActions(t, Options{EmergencyStop: func(context.Context, EmergencyStopRequest) (EmergencyStopReport, error) {
		called = true
		return EmergencyStopReport{StopID: "stop-1"}, nil
	}})
	ctx := context.Background()
	for name, factory := range map[string]core.FactoryID{"another factory": "sisf-other", "no factory": ""} {
		_, err := client.EmergencyStop(ctx, EmergencyStopRequest{FactoryID: factory, Actor: "user:owner", Reason: "runaway spend"})
		if err == nil || called {
			t.Fatalf("%s: EmergencyStop = %v, stop ran = %v; want a refusal and no stop", name, err, called)
		}
	}
	if _, err := client.EmergencyStop(ctx, EmergencyStopRequest{FactoryID: testFactory, Actor: "user:owner", Reason: "runaway spend"}); err != nil || !called {
		t.Fatalf("a stop of this factory = %v, ran = %v", err, called)
	}
}
