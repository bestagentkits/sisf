package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/core"
)

// The decision layer runs only when the edge's counters URL is set, and then
// only with the read-only counters token. The edge admin variables turn
// nothing on in the control zone.
func TestTheDecisionLayerNeedsTheEdgeCountersURLAndItsToken(t *testing.T) {
	t.Parallel()
	env := func(vars map[string]string) func(string) string { return func(k string) string { return vars[k] } }

	if cfg, err := decisionsFromEnv(env(nil)); err != nil || cfg != nil {
		t.Fatalf("without the edge counters URL: %+v, %v", cfg, err)
	}
	if cfg, err := decisionsFromEnv(env(map[string]string{"SISF_EDGE_ADMIN_URL": "http://127.0.0.1:7462", "EDGE_ADMIN_TOKEN": "edge-token"})); err != nil || cfg != nil {
		t.Fatalf("the edge admin variables configured the control zone: %+v, %v", cfg, err)
	}
	if _, err := decisionsFromEnv(env(map[string]string{wiring.EnvEdgeCountersURL: "http://127.0.0.1:7462"})); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("a URL without its token was accepted: %v", err)
	}
	cfg, err := decisionsFromEnv(env(map[string]string{wiring.EnvEdgeCountersURL: "http://127.0.0.1:7462", wiring.EnvEdgeCountersToken: "counts-token"}))
	if err != nil || cfg == nil || cfg.Counters == nil {
		t.Fatalf("the configured layer: %+v, %v", cfg, err)
	}
	counters, ok := cfg.Counters.(wiring.EdgeCounters)
	if !ok {
		t.Fatalf("counters are %T", cfg.Counters)
	}
	if _, ok := counters.Edge.(interface {
		SetSplit(context.Context, edge.Split) error
	}); ok {
		t.Fatalf("the control zone holds %T, which can change the split", counters.Edge)
	}
}

// The control zone's handle on the edge is typed so that changing the
// split cannot even be expressed through it.
func TestTheControlZonesEdgeHandleHasNoWayToChangeTheSplit(t *testing.T) {
	t.Parallel()
	field, ok := reflect.TypeFor[wiring.EdgeCounters]().FieldByName("Edge")
	if !ok {
		t.Fatal("wiring.EdgeCounters has no Edge field")
	}
	if field.Type.Kind() != reflect.Interface {
		t.Fatalf("the edge handle is a %s, want an interface", field.Type)
	}
	for i := range field.Type.NumMethod() {
		if name := field.Type.Method(i).Name; name != "Counts" {
			t.Errorf("the edge handle %s offers %s; it may only read counts", field.Type, name)
		}
	}
}
