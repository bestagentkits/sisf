package control

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// HealthStatus is the verdict on one zone.
type HealthStatus string

const (
	// HealthOK means the zone answered its probe.
	HealthOK HealthStatus = "ok"
	// HealthDown means the probe failed or timed out.
	HealthDown HealthStatus = "down"
	// HealthUnknown means no probe is configured for the zone. It counts as
	// unhealthy: a zone nobody checks is not assumed to be fine.
	HealthUnknown HealthStatus = "unknown"
)

// ZoneHealth is the health of one zone. Detail is a short reason that is
// safe to show to the Owner; the unauthenticated health endpoint omits it.
type ZoneHealth struct {
	Zone   core.Zone    `json:"zone"`
	Status HealthStatus `json:"status"`
	Detail string       `json:"detail,omitempty"`
}

// DefaultProbeTimeout bounds how long one zone probe may take.
const DefaultProbeTimeout = 2 * time.Second

// HealthProbe checks one zone from the outside. A nil result means the zone
// answered; any error means it is down.
type HealthProbe interface {
	Probe(ctx context.Context) error
}

// HealthProbeFunc adapts a function to HealthProbe.
type HealthProbeFunc func(ctx context.Context) error

// Probe calls f.
func (f HealthProbeFunc) Probe(ctx context.Context) error { return f(ctx) }

// healthChecker aggregates the three zones. Zone 3 is the process itself, so
// it is checked through its state store; Zones 1 and 2 are checked by probes.
type healthChecker struct {
	store   state.Store
	probes  map[core.Zone]HealthProbe
	timeout time.Duration
}

func newHealthChecker(store state.Store, probes map[core.Zone]HealthProbe, timeout time.Duration) (*healthChecker, error) {
	for zone, probe := range probes {
		if zone != core.ZoneApplication && zone != core.ZoneAgent {
			return nil, core.NewError(core.CodeInvalidArgument, "health probes cover the application and agent zones; the control zone is checked in-process")
		}
		if probe == nil {
			return nil, core.NewError(core.CodeInvalidArgument, "the health probe for the "+string(zone)+" zone is nil")
		}
	}
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	return &healthChecker{store: store, probes: probes, timeout: timeout}, nil
}

// check probes the three zones at the same time and returns them in zone
// order. A probe that outlives the timeout is reported down, whether or not
// it honors cancellation.
func (h *healthChecker) check(ctx context.Context) []ZoneHealth {
	zones := core.Zones()
	out := make([]ZoneHealth, len(zones))
	var wg sync.WaitGroup
	for i, zone := range zones {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = h.checkZone(ctx, zone)
		}()
	}
	wg.Wait()
	return out
}

func (h *healthChecker) checkZone(ctx context.Context, zone core.Zone) ZoneHealth {
	run := h.controlZone
	if zone != core.ZoneControl {
		probe, ok := h.probes[zone]
		if !ok {
			return ZoneHealth{Zone: zone, Status: HealthUnknown, Detail: "no health probe is configured"}
		}
		run = probe.Probe
	}
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	done := make(chan error, 1) // buffered so a late answer never blocks its goroutine
	go func() { done <- run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			return ZoneHealth{Zone: zone, Status: HealthDown, Detail: describeProbeError(err)}
		}
		return ZoneHealth{Zone: zone, Status: HealthOK}
	case <-ctx.Done():
		return ZoneHealth{Zone: zone, Status: HealthDown, Detail: "the health probe did not answer in time"}
	}
}

// controlZone proves the state store is open, migrated and answering.
func (h *healthChecker) controlZone(ctx context.Context) error {
	return h.store.View(ctx, func(context.Context, state.Tx) error { return nil })
}

// describeProbeError returns text safe to show to the Owner. A typed error's
// message is written for callers; any other error may carry addresses or
// credentials from a driver, so it is replaced.
func describeProbeError(err error) string {
	var typed *core.Error
	if errors.As(err, &typed) {
		return string(typed.Code) + ": " + typed.Message
	}
	return "the health probe failed"
}

// Health reports the three zones, in zone order.
func (s *Server) Health(ctx context.Context) []ZoneHealth { return s.health.check(ctx) }

// handleHealthz answers the unauthenticated liveness check. It reports each
// zone's verdict and the factory it belongs to, and nothing else; the
// reasons are for the Owner's log. The factory id is not secret: it lets a
// client on the same host tell this factory from another one answering on
// the same fixed port, before it sends a token or trusts a verdict.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	zones := map[core.Zone]HealthStatus{}
	status, code := "ok", http.StatusOK
	for _, z := range s.Health(r.Context()) {
		zones[z.Zone] = z.Status
		if z.Status != HealthOK {
			status, code = "degraded", http.StatusServiceUnavailable
			s.log.Warn("a zone is not healthy", "zone", string(z.Zone), "status", string(z.Status), "detail", z.Detail)
		}
	}
	writeJSON(w, code, map[string]any{"factory_id": s.cfg.FactoryID, "status": status, "zones": zones})
}
