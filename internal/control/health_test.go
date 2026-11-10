package control_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

type healthBody struct {
	Status string                             `json:"status"`
	Zones  map[core.Zone]control.HealthStatus `json:"zones"`
}

func TestHealthzAggregatesAllThreeZones(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		rec := e.do(httptest.NewRequest(http.MethodGet, "/healthz", nil))
		var body healthBody
		getJSON(t, rec, &body)
		if rec.Code != http.StatusOK || body.Status != "ok" || len(body.Zones) != 3 {
			t.Fatalf("healthz = %d %+v, want 200 with three healthy zones", rec.Code, body)
		}
		for zone, status := range body.Zones {
			if status != control.HealthOK {
				t.Errorf("zone %s = %s", zone, status)
			}
		}
	})
}

func TestHealthzReportsADownZoneWithoutItsReason(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		e.probes.set(core.ZoneAgent, func(context.Context) error {
			return errors.New("dial tcp 10.1.2.3:5432: password=hunter2 refused")
		})
		rec := e.do(httptest.NewRequest(http.MethodGet, "/healthz", nil))
		var body healthBody
		getJSON(t, rec, &body)
		if rec.Code != http.StatusServiceUnavailable || body.Status != "degraded" || body.Zones[core.ZoneAgent] != control.HealthDown {
			t.Fatalf("healthz = %d %+v, want 503 with the agent zone down", rec.Code, body)
		}
		if body.Zones[core.ZoneApplication] != control.HealthOK || body.Zones[core.ZoneControl] != control.HealthOK {
			t.Errorf("a healthy zone was reported down: %+v", body.Zones)
		}
		if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "10.1.2.3") {
			t.Errorf("the open endpoint leaked a probe error: %s", rec.Body.String())
		}

		// The Owner sees that the zone is down, but still not the raw error.
		detail := zoneOf(t, e.server.Health(context.Background()), core.ZoneAgent)
		if detail.Status != control.HealthDown || detail.Detail == "" || strings.Contains(detail.Detail, "hunter2") {
			t.Errorf("agent zone = %+v, want down with a safe reason", detail)
		}
	})
}

func TestTypedProbeErrorsKeepTheirMessage(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		e.probes.set(core.ZoneApplication, func(context.Context) error {
			return core.NewError(core.CodeUnavailable, "the application answered 503")
		})
		if got := zoneOf(t, e.server.Health(context.Background()), core.ZoneApplication).Detail; got != "UNAVAILABLE: the application answered 503" {
			t.Errorf("detail = %q", got)
		}
	})
}

func TestProbeThatNeverAnswersIsDown(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		// A short probe timeout makes the hung probes give up quickly.
		e := newEnv(t, db, func(c *control.Config) { c.ProbeTimeout = 30 * time.Millisecond })
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		// One probe honors cancellation, the other ignores it entirely.
		e.probes.set(core.ZoneApplication, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
		e.probes.set(core.ZoneAgent, func(context.Context) error { <-release; return nil })
		status := e.server.Health(context.Background())
		for _, zone := range []core.Zone{core.ZoneApplication, core.ZoneAgent} {
			if z := zoneOf(t, status, zone); z.Status != control.HealthDown {
				t.Errorf("zone %s = %+v, want down after the probe timeout", zone, z)
			}
		}
	})
}

func TestZoneWithoutAProbeIsUnknownAndUnhealthy(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		deps := e.deps
		deps.Probes = map[core.Zone]control.HealthProbe{core.ZoneApplication: e.probes.probe(core.ZoneApplication)}
		server, err := control.New(e.cfg, deps)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		var body healthBody
		getJSON(t, rec, &body)
		if rec.Code != http.StatusServiceUnavailable || body.Zones[core.ZoneAgent] != control.HealthUnknown {
			t.Fatalf("healthz = %d %+v, want 503 with the agent zone unknown", rec.Code, body)
		}
	})
}

func TestControlZoneIsDownUntilTheStoreIsMigrated(t *testing.T) {
	t.Parallel()
	sqlstoretest.RunUnmigrated(t, func(t *testing.T, db *sqlstoretest.Database) {
		e := newEnv(t, db)
		rec := e.do(httptest.NewRequest(http.MethodGet, "/healthz", nil))
		var body healthBody
		getJSON(t, rec, &body)
		if rec.Code != http.StatusServiceUnavailable || body.Zones[core.ZoneControl] != control.HealthDown {
			t.Fatalf("healthz = %d %+v, want 503 with the control zone down", rec.Code, body)
		}
	})
}

func TestProbesForTheControlZoneAreRejected(t *testing.T) {
	t.Parallel()
	withServer(t, func(t *testing.T, e *env) {
		for name, probes := range map[string]map[core.Zone]control.HealthProbe{
			"control zone": {core.ZoneControl: e.probes.probe(core.ZoneApplication)},
			"nil probe":    {core.ZoneAgent: nil},
		} {
			deps := e.deps
			deps.Probes = probes
			if _, err := control.New(e.cfg, deps); !core.HasCode(err, core.CodeInvalidArgument) {
				t.Errorf("%s: error = %v, want INVALID_ARGUMENT", name, err)
			}
		}
	})
}

func zoneOf(t *testing.T, zones []control.ZoneHealth, zone core.Zone) control.ZoneHealth {
	t.Helper()
	for _, z := range zones {
		if z.Zone == zone {
			return z
		}
	}
	t.Fatalf("zone %s missing from %+v", zone, zones)
	return control.ZoneHealth{}
}
