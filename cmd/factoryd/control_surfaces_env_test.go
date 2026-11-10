package main

import (
	"testing"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/pkg/core"
)

// The decision layer runs only when the edge's admin URL is set, and then
// only with the admin token.
func TestTheDecisionLayerNeedsTheEdgeAdminURLAndItsToken(t *testing.T) {
	t.Parallel()
	env := func(vars map[string]string) func(string) string { return func(k string) string { return vars[k] } }

	if cfg, err := decisionsFromEnv(env(nil)); err != nil || cfg != nil {
		t.Fatalf("without the edge admin URL: %+v, %v", cfg, err)
	}
	if _, err := decisionsFromEnv(env(map[string]string{wiring.EnvEdgeAdminURL: "http://127.0.0.1:7462"})); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("a URL without its token was accepted: %v", err)
	}
	cfg, err := decisionsFromEnv(env(map[string]string{wiring.EnvEdgeAdminURL: "http://127.0.0.1:7462", wiring.EnvEdgeAdminToken: "edge-token"}))
	if err != nil || cfg == nil || cfg.Counters == nil {
		t.Fatalf("the configured layer: %+v, %v", cfg, err)
	}
}
