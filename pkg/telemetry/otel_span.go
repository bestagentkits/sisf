package telemetry

import (
	"slices"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// otelSpan is a Span backed by an OpenTelemetry span. Values are redacted
// before they are recorded, and the six required attributes set when the
// span started cannot be overwritten afterwards.
type otelSpan struct {
	span    trace.Span
	adapter *OTel
}

var _ Span = (*otelSpan)(nil)

// SetAttribute records a redacted value. A required key is ignored: the
// span keeps the labels it was validated with.
func (s *otelSpan) SetAttribute(key, value string) {
	if key == "" || slices.Contains(RequiredAttributes(), key) {
		return
	}
	s.span.SetAttributes(attribute.String(key, s.adapter.redactor.Redact(value)))
}

// RecordError marks the span failed with a redacted message. The error
// itself is not handed to OpenTelemetry, which would record its text as is.
func (s *otelSpan) RecordError(err error) {
	if err == nil {
		return
	}
	message := s.adapter.redactor.Redact(err.Error())
	s.span.AddEvent("exception",
		trace.WithTimestamp(s.adapter.clock.Now()),
		trace.WithAttributes(attribute.String("exception.message", message)))
	s.span.SetStatus(codes.Error, message)
}

// End finishes the span at the adapter's clock.
func (s *otelSpan) End() { s.span.End(trace.WithTimestamp(s.adapter.clock.Now())) }

// refusedSpan stands in for a span whose attributes did not validate. It
// records nothing, so nothing half-labelled is exported.
type refusedSpan struct{}

func (refusedSpan) SetAttribute(string, string) {}
func (refusedSpan) RecordError(error)           {}
func (refusedSpan) End()                        {}
