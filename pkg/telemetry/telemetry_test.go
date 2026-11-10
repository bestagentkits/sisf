package telemetry_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/core"
	"github.com/bestagentkits/sisf/pkg/telemetry"
)

const digest = core.ReleaseDigest("sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

func runAttributes() telemetry.Attributes {
	return telemetry.Attributes{
		FactoryID:     "acme-factory",
		DeploymentID:  "laptop-1",
		Zone:          core.ZoneAgent,
		ReleaseDigest: digest,
		RunID:         "run-0001",
		IssueRef:      core.IssueRef{Owner: "acme", Repo: "app", Number: 7},
	}
}

func TestEverySignalCarriesTheSixRequiredAttributes(t *testing.T) {
	want := []string{"factory_id", "deployment_id", "zone", "release_digest", "run_id", "issue_ref"}
	if got := telemetry.RequiredAttributes(); !slices.Equal(got, want) {
		t.Fatalf("RequiredAttributes() = %v, want %v", got, want)
	}
	got := runAttributes().Map()
	if len(got) != len(want) {
		t.Fatalf("Map() = %v", got)
	}
	if got["factory_id"] != "acme-factory" || got["deployment_id"] != "laptop-1" || got["zone"] != "agent" ||
		got["release_digest"] != string(digest) || got["run_id"] != "run-0001" || got["issue_ref"] != "acme/app#7" {
		t.Fatalf("Map() = %v", got)
	}
	outsideARun := telemetry.Attributes{FactoryID: "acme-factory", DeploymentID: "laptop-1", Zone: core.ZoneApplication, ReleaseDigest: digest}
	blank := outsideARun.Map()
	for _, key := range want {
		if _, ok := blank[key]; !ok {
			t.Errorf("key %q must be present even when it has no value", key)
		}
	}
	if blank["run_id"] != "" || blank["issue_ref"] != "" {
		t.Errorf("signals outside a run carry empty run and issue values, got %v", blank)
	}
}

func TestAttributesValidation(t *testing.T) {
	if err := runAttributes().Validate(); err != nil {
		t.Fatalf("valid attributes rejected: %v", err)
	}
	outsideARun := runAttributes()
	outsideARun.RunID, outsideARun.IssueRef = "", core.IssueRef{}
	if err := outsideARun.Validate(); err != nil {
		t.Fatalf("application traffic has no run or issue: %v", err)
	}
	shared := runAttributes()
	shared.Zone = core.ZoneShared
	if err := shared.Validate(); err != nil {
		t.Fatalf("shared units emit telemetry too: %v", err)
	}
	cases := map[string]func(*telemetry.Attributes){
		"no factory":         func(a *telemetry.Attributes) { a.FactoryID = "" },
		"no deployment":      func(a *telemetry.Attributes) { a.DeploymentID = "" },
		"unknown zone":       func(a *telemetry.Attributes) { a.Zone = "zone4" },
		"release not pinned": func(a *telemetry.Attributes) { a.ReleaseDigest = "latest" },
		"malformed run":      func(a *telemetry.Attributes) { a.RunID = "Run 1" },
		"malformed issue":    func(a *telemetry.Attributes) { a.IssueRef = core.IssueRef{Owner: "acme"} },
	}
	for name, mutate := range cases {
		attrs := runAttributes()
		mutate(&attrs)
		if core.CodeOf(attrs.Validate()) != core.CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, attrs.Validate())
		}
	}
}

func TestSignalKindsAreAClosedSet(t *testing.T) {
	for _, k := range []telemetry.Kind{telemetry.KindLog, telemetry.KindMetric, telemetry.KindEvent} {
		if !k.Valid() {
			t.Errorf("kind %q must be valid", k)
		}
	}
	if telemetry.Kind("trace").Valid() || telemetry.Kind("").Valid() {
		t.Error("unknown kinds must be rejected")
	}
}

// adapterStub proves the contract can be met with the exported types alone.
type adapterStub struct{ emitted []telemetry.Event }

type spanStub struct{}

func (spanStub) SetAttribute(string, string) {}
func (spanStub) RecordError(error)           {}
func (spanStub) End()                        {}

func (a *adapterStub) StartSpan(ctx context.Context, _ string, _ telemetry.Attributes) (context.Context, telemetry.Span) {
	return ctx, spanStub{}
}
func (a *adapterStub) Emit(_ context.Context, e telemetry.Event) error {
	if err := e.Attributes.Validate(); err != nil {
		return err
	}
	a.emitted = append(a.emitted, e)
	return nil
}
func (a *adapterStub) Shutdown(context.Context) error { return nil }
func (a *adapterStub) Query(context.Context, telemetry.Query) ([]telemetry.Event, error) {
	return a.emitted, nil
}
func (a *adapterStub) Redact(text string) string { return "[REDACTED]" }

func TestAdapterContractIsImplementable(t *testing.T) {
	stub := &adapterStub{}
	var adapter telemetry.Adapter = stub
	var reader telemetry.Reader = stub
	var redactor telemetry.Redactor = stub
	ctx, span := adapter.StartSpan(context.Background(), "release.monitor", runAttributes())
	defer span.End()
	if err := adapter.Emit(ctx, telemetry.Event{Kind: telemetry.KindMetric, Name: "http.server.errors", Value: 3}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("a signal without the required attributes must be refused, got %v", err)
	}
	event := telemetry.Event{Kind: telemetry.KindLog, Name: "run.started", At: time.Now(), Attributes: runAttributes(), Body: redactor.Redact("token=abc")}
	if err := adapter.Emit(ctx, event); err != nil {
		t.Fatal(err)
	}
	got, err := reader.Query(ctx, telemetry.Query{RunID: "run-0001"})
	if err != nil || len(got) != 1 || got[0].Body != "[REDACTED]" {
		t.Fatalf("Query = %+v, %v", got, err)
	}
}
