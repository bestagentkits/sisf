package wiring_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/github"
)

// hostDispatcher builds a dispatcher the way factoryd does, named after
// this host.
func hostDispatcher(t *testing.T, f *harness.Factory) *dispatcher.Dispatcher {
	t.Helper()
	d, err := wiring.NewDispatcher(wiring.DispatcherConfig{Manifest: f.Manifest, Store: f.AgentStore, Agent: f.Agent,
		Clock: f.Clock, PollInterval: harness.PollInterval, LeaseTTL: harness.LeaseTTL})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// Two dispatcher processes on one host do not both act as the active
// dispatcher: only one holds the lease and claims, however they interleave.
func TestTwoDispatchersOnOneHostDoNotBothLead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	f.OpenIssue("maintainer", "MEMBER", "Health check fails", bugBody, github.LabelReady, github.LabelKindBug)
	f.Pass()

	a, b := hostDispatcher(t, f), hostDispatcher(t, f)
	claims := map[string]int{}
	for range 4 {
		for _, d := range []*dispatcher.Dispatcher{a, b} {
			res, err := d.Tick(ctx)
			if err != nil {
				t.Fatal(err)
			}
			claims[d.Health().Owner] += len(res.Claimed)
		}
		if a.Health().Leader && b.Health().Leader {
			t.Fatal("both dispatchers act as the active one")
		}
		f.Clock.Advance(harness.PollInterval)
	}
	if !a.Health().Leader || claims[a.Health().Owner] != 1 || claims[b.Health().Owner] != 0 {
		t.Fatalf("the first dispatcher should lead and claim once: claims %v", claims)
	}
}

// The name says which host a dispatcher runs on.
func TestADispatcherIsNamedAfterItsHost(t *testing.T) {
	t.Parallel()
	if owner := wiring.DispatcherOwner(); !strings.HasPrefix(owner, "dispatcher@") || owner == wiring.DispatcherOwner() {
		t.Fatalf("owner %q must name the host and differ per process", owner)
	}
}
