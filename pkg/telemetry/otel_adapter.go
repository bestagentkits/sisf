package telemetry

import (
	"context"
	"strings"
	"sync/atomic"
	"unicode"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/bestagentkits/sisf/pkg/core"
)

// tracerName is the instrumentation scope of every factory span.
const tracerName = "github.com/bestagentkits/sisf"

// maxNameLen bounds span and event names, which come from code, not data.
const maxNameLen = 128

// OTelConfig configures the OpenTelemetry adapter.
type OTelConfig struct {
	// Exporter receives finished spans in batches. Required.
	Exporter sdktrace.SpanExporter
	// Redactor is applied to every text value before it is recorded.
	// Required: without one, text would leave unredacted.
	Redactor Redactor
	// Clock stamps spans and events. Nil means the wall clock.
	Clock core.Clock
	// Store keeps normalized events for Query. Nil means a MemoryStore of
	// the default capacity.
	Store EventStore
}

// OTel is the Adapter and Reader backed by OpenTelemetry. Spans go to the
// configured exporter; normalized events are kept in the store and also
// attached to the active span. It is safe for concurrent use.
type OTel struct {
	provider *sdktrace.TracerProvider
	tracer   trace.Tracer
	redactor Redactor
	clock    core.Clock
	store    EventStore
	refused  atomic.Int64
}

var (
	_ Adapter = (*OTel)(nil)
	_ Reader  = (*OTel)(nil)
)

// NewOTel builds the adapter.
func NewOTel(cfg OTelConfig) (*OTel, error) {
	if cfg.Exporter == nil || cfg.Redactor == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "telemetry needs a span exporter and a redactor")
	}
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	if cfg.Store == nil {
		cfg.Store = NewMemoryStore(DefaultStoreCapacity)
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(cfg.Exporter))
	return &OTel{
		provider: provider,
		tracer:   provider.Tracer(tracerName),
		redactor: cfg.Redactor,
		clock:    cfg.Clock,
		store:    cfg.Store,
	}, nil
}

// StartSpan starts a span carrying the six required attributes. A span
// whose attributes or name do not validate is refused: the returned span
// records nothing and the refusal is counted.
func (a *OTel) StartSpan(ctx context.Context, name string, attrs Attributes) (context.Context, Span) {
	if attrs.Validate() != nil || validName(name) != nil {
		a.refused.Add(1)
		return ctx, refusedSpan{}
	}
	ctx, span := a.tracer.Start(ctx, name,
		trace.WithTimestamp(a.clock.Now()),
		trace.WithAttributes(required(attrs)...))
	return ctx, &otelSpan{span: span, adapter: a}
}

// Emit validates, redacts and stores one normalized event, and adds it to
// the span active in ctx, if any.
func (a *OTel) Emit(ctx context.Context, e Event) error {
	if err := validEvent(e); err != nil {
		a.refused.Add(1)
		return err
	}
	if e.At.IsZero() {
		e.At = a.clock.Now()
	}
	e.Body = a.redactor.Redact(e.Body)
	fields := make(map[string]string, len(e.Fields))
	for k, v := range e.Fields {
		fields[k] = a.redactor.Redact(v)
	}
	e.Fields = fields
	if err := a.store.Append(ctx, e); err != nil {
		return core.NewError(core.CodeUnavailable, "telemetry event could not be stored").WithCause(err)
	}
	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		kv := append(required(e.Attributes), attribute.String("kind", string(e.Kind)))
		span.AddEvent(e.Name, trace.WithTimestamp(e.At), trace.WithAttributes(kv...))
	}
	return nil
}

// Query reads stored events back.
func (a *OTel) Query(ctx context.Context, q Query) ([]Event, error) { return a.store.Query(ctx, q) }

// ForceFlush exports every finished span that is still buffered.
func (a *OTel) ForceFlush(ctx context.Context) error { return a.provider.ForceFlush(ctx) }

// Shutdown flushes buffered spans and stops the exporter.
func (a *OTel) Shutdown(ctx context.Context) error { return a.provider.Shutdown(ctx) }

// Refused returns how many spans and events were refused for failing
// validation since the adapter started.
func (a *OTel) Refused() int64 { return a.refused.Load() }

// required renders the six required attributes, empty values included.
func required(attrs Attributes) []attribute.KeyValue {
	m := attrs.Map()
	out := make([]attribute.KeyValue, 0, len(m))
	for _, key := range RequiredAttributes() {
		out = append(out, attribute.String(key, m[key]))
	}
	return out
}

func validEvent(e Event) error {
	if !e.Kind.Valid() {
		return core.NewError(core.CodeInvalidArgument, "an event needs the kind log, metric or event")
	}
	if err := validName(e.Name); err != nil {
		return err
	}
	if err := e.Attributes.Validate(); err != nil {
		return err
	}
	for _, key := range RequiredAttributes() {
		if _, forged := e.Fields[key]; forged {
			return core.NewError(core.CodeInvalidArgument, "event fields must not carry a required attribute key")
		}
	}
	return nil
}

func validName(name string) error {
	if strings.TrimSpace(name) == "" || len(name) > maxNameLen ||
		strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) }) {
		return core.NewError(core.CodeInvalidArgument, "a signal name must be 1-128 printable characters")
	}
	return nil
}
