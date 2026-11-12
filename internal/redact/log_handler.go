package redact

import (
	"context"
	"log/slog"
)

// logHandler redacts a record's message and attribute values before the
// next handler writes them.
type logHandler struct {
	next slog.Handler
	r    *Redactor
}

// NewLogHandler wraps next so that nothing it writes carries a secret or
// personal data. Attribute keys are kept; values of any type are rendered
// to text and redacted.
func NewLogHandler(next slog.Handler, r *Redactor) slog.Handler {
	return &logHandler{next: next, r: r}
}

func (h *logHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *logHandler) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, h.r.Redact(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.attr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = h.attr(a)
	}
	return &logHandler{next: h.next.WithAttrs(clean), r: h.r}
}

func (h *logHandler) WithGroup(name string) slog.Handler {
	return &logHandler{next: h.next.WithGroup(name), r: h.r}
}

// attr redacts one attribute, descending into groups.
func (h *logHandler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindGroup:
		group := v.Group()
		clean := make([]any, len(group))
		for i, g := range group {
			clean[i] = h.attr(g)
		}
		return slog.Group(a.Key, clean...)
	case slog.KindString, slog.KindAny:
		return slog.String(a.Key, h.r.Redact(v.String()))
	default:
		// Numbers, booleans, durations and times carry no free text.
		return slog.Attr{Key: a.Key, Value: v}
	}
}
