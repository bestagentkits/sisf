package killswitch

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/core"
)

// fakeSupervisor answers emergency stops on an ephemeral loopback port, as
// the supervisor of the test factory.
func fakeSupervisor(t *testing.T) (addr string, stops *atomic.Int32) {
	return fakeSupervisorOf(t, testFactory, "ed25519:this-install")
}

// fakeSupervisorOf answers status and emergency stops as the supervisor of
// factory, whose release was signed by keyID.
func fakeSupervisorOf(t *testing.T, factory core.FactoryID, keyID string) (addr string, stops *atomic.Int32) {
	t.Helper()
	stops = &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/status":
			_ = json.NewEncoder(w).Encode(supervisor.Status{FactoryID: factory, SelfCheck: supervisor.SelfCheckStatus{Passed: true, ManifestKeyID: keyID}})
		case r.Method == http.MethodPost && r.URL.Path == supervisor.EmergencyStopPath:
			stops.Add(1)
			_ = json.NewEncoder(w).Encode(supervisor.EmergencyStopReport{StopID: "stop-sup", PublishingFrozen: true, RunsCancelled: []core.RunID{"run-1"}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://"), stops
}

// deadAddr is a loopback address nothing listens on.
func deadAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func operatorRequest(dsn, addr string) OperatorRequest {
	var secret core.SecretValue
	if dsn != "" {
		secret = core.NewSecretValue([]byte(dsn))
	}
	return OperatorRequest{FactoryID: testFactory, Actor: "user:owner", Reason: "runaway spend", StateDSN: secret, AdminAddr: addr}
}

func TestAnOperatorStopUsesTheStoreAndTheSupervisor(t *testing.T) {
	t.Parallel()
	dsn := filepath.Join(t.TempDir(), "state.db")
	f := newFactoryAt(t, dsn)
	f.ready(1)
	addr, stops := fakeSupervisor(t)
	req := operatorRequest(dsn, addr)
	req.Clock = f.clock
	report, err := StopFromOperator(context.Background(), req)
	if err != nil || report.Store == nil || report.Supervisor == nil || stops.Load() != 1 {
		t.Fatalf("StopFromOperator = %+v, %v (supervisor asked %d times)", report, err, stops.Load())
	}
	if frozen, err := f.kill().Frozen(context.Background()); !frozen || err != nil {
		t.Fatalf("Frozen = %v, %v", frozen, err)
	}
	if _, err := f.claim(1, "dispatcher-1", time.Hour); !core.HasCode(err, core.CodeLeaseHeld) {
		t.Fatalf("a claim after the operator stop = %v, want LEASE_HELD", err)
	}
	if err := LiftFromOperator(context.Background(), testFactory, req.StateDSN, f.clock, "user:owner", "resolved"); err != nil {
		t.Fatalf("LiftFromOperator: %v", err)
	}
	if frozen, _ := f.kill().Frozen(context.Background()); frozen {
		t.Fatal("the factory is still frozen after the lift")
	}
}

func TestAnOperatorStopWithoutASupervisorStillFreezesButFails(t *testing.T) {
	t.Parallel()
	dsn := filepath.Join(t.TempDir(), "state.db")
	f := newFactoryAt(t, dsn)
	req := operatorRequest(dsn, deadAddr(t))
	req.Clock = f.clock
	report, err := StopFromOperator(context.Background(), req)
	if !core.HasCode(err, core.CodeUnavailable) || report.Store == nil || report.Supervisor != nil || len(report.Failures) != 1 {
		t.Fatalf("StopFromOperator = %+v, %v, want the store stop and an UNAVAILABLE failure", report, err)
	}
	if frozen, _ := f.kill().Frozen(context.Background()); !frozen {
		t.Fatal("the store stop did not freeze publishing")
	}
}

func TestAnOperatorStopNeedsARouteAndAReason(t *testing.T) {
	t.Parallel()
	if _, err := StopFromOperator(context.Background(), operatorRequest("", "")); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("no route = %v", err)
	}
	req := operatorRequest("", "127.0.0.1:1")
	req.Reason = ""
	if _, err := StopFromOperator(context.Background(), req); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("no reason = %v", err)
	}
	report, err := StopFromOperator(context.Background(), operatorRequest("", "203.0.113.5:7468"))
	if !core.HasCode(err, core.CodeUnavailable) || report.Supervisor != nil {
		t.Errorf("a remote supervisor address = %+v, %v, want it refused", report, err)
	}
	unprepared := operatorRequest(filepath.Join(t.TempDir(), "fresh.db"), "")
	if report, err := StopFromOperator(context.Background(), unprepared); !core.HasCode(err, core.CodeUnavailable) || report.Store != nil {
		t.Errorf("an unprepared store = %+v, %v", report, err)
	}
}

// A stop is never sent to the supervisor of another factory, or of another
// install of the same factory, that answers on the admin address; the store
// route of this install still stops it.
func TestAnOperatorStopIsNeverSentToAnotherFactorysSupervisor(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		factory    core.FactoryID
		key, owned string
	}{
		"another factory":                 {"sisf-other", "ed25519:this-install", ""},
		"another install of this factory": {testFactory, "ed25519:other-install", "ed25519:this-install"},
	} {
		dsn := filepath.Join(t.TempDir(), "state.db")
		f := newFactoryAt(t, dsn)
		f.ready(1)
		addr, stops := fakeSupervisorOf(t, tc.factory, tc.key)
		req := operatorRequest(dsn, addr)
		req.Clock, req.AdminKeyID = f.clock, tc.owned
		report, err := StopFromOperator(context.Background(), req)
		if stops.Load() != 0 || report.Supervisor != nil {
			t.Fatalf("%s: the stop was sent to the supervisor (%d times)", name, stops.Load())
		}
		if err == nil || len(report.Failures) != 1 || !strings.Contains(report.Failures[0], "nothing was sent") {
			t.Errorf("%s: StopFromOperator = %+v, %v; want the supervisor route reported as refused", name, report, err)
		}
		if report.Store == nil {
			t.Errorf("%s: the store route did not stop the factory", name)
		}
	}
}
