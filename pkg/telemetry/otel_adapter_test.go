package telemetry_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/telemetry"
)

// fixedClock is a core.Clock that only moves when a test moves it.
type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }
func (c *fixedClock) After(d time.Duration) <-chan time.Time {
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

// maskSecret stands in for the factory redactor: it hides one known value.
type maskSecret struct{}

func (maskSecret) Redact(text string) string {
	return strings.ReplaceAll(text, "hunter2-secret", "[REDACTED]")
}

func newAdapter(t *testing.T) (*telemetry.OTel, *tracetest.InMemoryExporter, *fixedClock) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	clock := &fixedClock{now: time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)}
	adapter, err := telemetry.NewOTel(telemetry.OTelConfig{Exporter: exporter, Redactor: maskSecret{}, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Shutdown(context.Background()) })
	return adapter, exporter, clock
}

func flushedSpans(t *testing.T, a *telemetry.OTel, e *tracetest.InMemoryExporter) tracetest.SpanStubs {
	t.Helper()
	if err := a.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e.GetSpans()
}

func spanAttrs(s tracetest.SpanStub) map[string]string {
	out := map[string]string{}
	for _, kv := range s.Attributes {
		out[string(kv.Key)] = kv.Value.AsString()
	}
	return out
}

func TestSpansCarryTheSixRequiredAttributesAndRedactedValues(t *testing.T) {
	t.Parallel()
	adapter, exporter, clock := newAdapter(t)
	var _ telemetry.Adapter = adapter
	var _ telemetry.Reader = adapter

	_, span := adapter.StartSpan(context.Background(), "release.monitor", runAttributes())
	span.SetAttribute("endpoint", "GET /x?token=hunter2-secret")
	span.SetAttribute(telemetry.AttrRunID, "run-forged")
	span.RecordError(errors.New("upstream said hunter2-secret"))
	clock.now = clock.now.Add(time.Second)
	span.End()

	spans := flushedSpans(t, adapter, exporter)
	if len(spans) != 1 {
		t.Fatalf("want one span, got %d", len(spans))
	}
	got := spanAttrs(spans[0])
	for key, want := range runAttributes().Map() {
		if got[key] != want {
			t.Errorf("attribute %s = %q, want %q", key, got[key], want)
		}
	}
	if strings.Contains(got["endpoint"], "hunter2-secret") {
		t.Errorf("attribute value was not redacted: %q", got["endpoint"])
	}
	if strings.Contains(spans[0].Status.Description, "hunter2-secret") || spans[0].Status.Description == "" {
		t.Errorf("error status must be set and redacted, got %q", spans[0].Status.Description)
	}
	for _, ev := range spans[0].Events {
		for _, kv := range ev.Attributes {
			if strings.Contains(kv.Value.AsString(), "hunter2-secret") {
				t.Errorf("error event leaked a secret: %v", kv)
			}
		}
	}
	if d := spans[0].EndTime.Sub(spans[0].StartTime); d != time.Second {
		t.Errorf("span times must come from the injected clock, duration %v", d)
	}
}

func TestSpanOutsideARunStillEmitsEveryKey(t *testing.T) {
	t.Parallel()
	adapter, exporter, _ := newAdapter(t)
	attrs := runAttributes()
	attrs.RunID, attrs.IssueRef = "", core.IssueRef{}
	_, span := adapter.StartSpan(context.Background(), "http.request", attrs)
	span.End()
	got := spanAttrs(flushedSpans(t, adapter, exporter)[0])
	for _, key := range telemetry.RequiredAttributes() {
		if _, ok := got[key]; !ok {
			t.Errorf("key %s missing", key)
		}
	}
}

func TestSpanWithInvalidAttributesIsRefused(t *testing.T) {
	t.Parallel()
	adapter, exporter, _ := newAdapter(t)
	attrs := runAttributes()
	attrs.ReleaseDigest = "latest"
	_, span := adapter.StartSpan(context.Background(), "release.monitor", attrs)
	span.SetAttribute("k", "v")
	span.RecordError(errors.New("boom"))
	span.End()
	if spans := flushedSpans(t, adapter, exporter); len(spans) != 0 {
		t.Fatalf("a half-labelled span must not be exported, got %d", len(spans))
	}
	if adapter.Refused() != 1 {
		t.Errorf("refusals must be counted, got %d", adapter.Refused())
	}
}

func TestEventsAreValidatedRedactedStampedAndReadable(t *testing.T) {
	t.Parallel()
	adapter, exporter, clock := newAdapter(t)
	ctx, span := adapter.StartSpan(context.Background(), "run", runAttributes())
	err := adapter.Emit(ctx, telemetry.Event{
		Kind: telemetry.KindLog, Name: "agent.output", Attributes: runAttributes(),
		Body: "password is hunter2-secret", Fields: map[string]string{"cmd": "echo hunter2-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	span.End()
	got, err := adapter.Query(ctx, telemetry.Query{RunID: "run-0001"})
	if err != nil || len(got) != 1 {
		t.Fatalf("Query = %v, %v", got, err)
	}
	if strings.Contains(got[0].Body, "hunter2-secret") || strings.Contains(got[0].Fields["cmd"], "hunter2-secret") {
		t.Errorf("stored event leaked a secret: %+v", got[0])
	}
	if !got[0].At.Equal(clock.now) {
		t.Errorf("an event without a time is stamped by the clock, got %v", got[0].At)
	}
	spans := flushedSpans(t, adapter, exporter)
	if len(spans) != 1 || len(spans[0].Events) != 1 || spans[0].Events[0].Name != "agent.output" {
		t.Fatalf("the event must also be attached to the active span: %+v", spans)
	}
}

func TestEventsThatWouldBeHalfLabelledOrMalformedAreRefused(t *testing.T) {
	t.Parallel()
	adapter, _, _ := newAdapter(t)
	valid := telemetry.Event{Kind: telemetry.KindMetric, Name: "http.server.errors", Attributes: runAttributes(), Value: 1}
	cases := map[string]func(*telemetry.Event){
		"missing attributes":       func(e *telemetry.Event) { e.Attributes = telemetry.Attributes{} },
		"unknown kind":             func(e *telemetry.Event) { e.Kind = "trace" },
		"blank name":               func(e *telemetry.Event) { e.Name = " " },
		"field forges an attr":     func(e *telemetry.Event) { e.Fields = map[string]string{telemetry.AttrZone: "control"} },
		"name with control chars":  func(e *telemetry.Event) { e.Name = "a\nb" },
		"name longer than allowed": func(e *telemetry.Event) { e.Name = strings.Repeat("a", 200) },
	}
	for name, mutate := range cases {
		e := valid
		mutate(&e)
		if err := adapter.Emit(context.Background(), e); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, err)
		}
	}
	if got, _ := adapter.Query(context.Background(), telemetry.Query{}); len(got) != 0 {
		t.Errorf("refused events must not be stored, got %d", len(got))
	}
	if err := adapter.Emit(context.Background(), valid); err != nil {
		t.Fatalf("the valid event was refused: %v", err)
	}
}

func TestAdapterNeedsAnExporterAndARedactor(t *testing.T) {
	t.Parallel()
	if _, err := telemetry.NewOTel(telemetry.OTelConfig{Redactor: maskSecret{}}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("no exporter: got %v", err)
	}
	if _, err := telemetry.NewOTel(telemetry.OTelConfig{Exporter: tracetest.NewInMemoryExporter()}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("no redactor: text would be emitted unredacted, got %v", err)
	}
}
