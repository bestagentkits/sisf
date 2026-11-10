package decision_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/decision"
	"github.com/bestagentkits/sisf/internal/redact"
	"github.com/bestagentkits/sisf/pkg/core"
	pkgdecision "github.com/bestagentkits/sisf/pkg/decision"
)

func TestLayerRefusesAnIncompleteConfiguration(t *testing.T) {
	t.Parallel()
	valid := func() decision.Config {
		return decision.Config{Source: newCounters(), Sink: &issueSink{}, Redactor: redact.New(redact.Config{}),
			Policy: policyRefs(), Rules: decision.DefaultRules()}
	}
	if _, err := decision.New(valid()); err != nil {
		t.Fatalf("valid configuration refused: %v", err)
	}
	cases := map[string]struct {
		mutate func(*decision.Config)
		code   core.ErrorCode
	}{
		"no source":          {func(c *decision.Config) { c.Source = nil }, core.CodeInvalidArgument},
		"no sink":            {func(c *decision.Config) { c.Sink = nil }, core.CodeInvalidArgument},
		"no redactor":        {func(c *decision.Config) { c.Redactor = nil }, core.CodeInvalidArgument},
		"unpinned policy":    {func(c *decision.Config) { c.Policy.DecisionSHA = "main" }, core.CodePolicyHold},
		"no minimum samples": {func(c *decision.Config) { c.Rules.MinSamples = 0 }, core.CodeInvalidArgument},
		"threshold of 100%":  {func(c *decision.Config) { c.Rules.ErrorRateThreshold = 1 }, core.CodeInvalidArgument},
		"no delta":           {func(c *decision.Config) { c.Rules.MinDelta = 0 }, core.CodeInvalidArgument},
		"short memory":       {func(c *decision.Config) { c.Rules.BaselineWindows = 1 }, core.CodeInvalidArgument},
		"no endpoints":       {func(c *decision.Config) { c.Rules.MaxEndpoints = 0 }, core.CodeInvalidArgument},
	}
	for name, c := range cases {
		cfg := valid()
		c.mutate(&cfg)
		if _, err := decision.New(cfg); !core.HasCode(err, c.code) {
			t.Errorf("%s: want %s, got %v", name, c.code, err)
		}
	}
}

func TestUnreadableCountersAreAnError(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.counters.err = errBoom
	if _, err := f.layer.Run(context.Background()); !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("want %s, got %v", core.CodeUnavailable, err)
	}
}

func TestFailedDeliveryIsRetriedAndNeverDoubled(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.baseline(t, "GET /a")
	f.window(t, "GET /a", 1000, 100)
	f.sink.err = errBoom
	f.counters.add("GET /a", 1000, 100)
	f.clock.After(time.Minute)
	outs, err := f.layer.Run(context.Background())
	if !core.HasCode(err, core.CodeUnavailable) || len(outs) != 1 || outs[0].NewProposal {
		t.Fatalf("a failed delivery must be reported: %+v, %v", outs, err)
	}
	f.sink.err = nil
	if o := f.window(t, "GET /a", 1000, 100); !o.NewProposal || f.sink.count() != 1 {
		t.Fatalf("the proposal is offered again on the next run: %+v", o)
	}
	if o := f.window(t, "GET /a", 1000, 100); o.NewProposal || f.sink.count() != 1 {
		t.Fatalf("and then never again: %+v", o)
	}
}

type brokenStore struct{}

func (brokenStore) Status(context.Context, string) (decision.ProposalStatus, error) {
	return decision.StatusNone, errBoom
}
func (brokenStore) Record(context.Context, pkgdecision.Proposal) error { return errBoom }

func TestUnreadableProposalStoreProposesNothing(t *testing.T) {
	t.Parallel()
	counters, sink := newCounters(), &issueSink{}
	clock := &manualClock{now: time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)}
	layer, err := decision.New(decision.Config{Source: counters, Sink: sink, Store: brokenStore{}, Clock: clock,
		Redactor: redact.New(redact.Config{}), Policy: policyRefs(), Rules: decision.DefaultRules()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := layer.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	counters.add("GET /a", 1000, 900)
	clock.After(time.Minute)
	outs, err := layer.Run(context.Background())
	if !core.HasCode(err, core.CodeUnavailable) || len(outs) != 1 ||
		outs[0].Verdict != pkgdecision.VerdictCollectMoreEvidence || sink.count() != 0 {
		t.Fatalf("an unknown dedup state must fail closed: %+v, %v", outs, err)
	}
}

func TestDismissingAnUnknownProposalIsNotFound(t *testing.T) {
	t.Parallel()
	if err := decision.NewMemoryProposals().Dismiss(context.Background(), "nope"); !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("got %v", err)
	}
}
