// Package observe gives each factory process its logger and its telemetry,
// both redacted: every log record and every span passes the redactor
// before it is written, so neither carries a secret the process holds or
// personal data.
package observe

import (
	"context"
	"io"
	"log/slog"
	"strings"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/bestagentkits/sisf/internal/redact"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/telemetry"
)

// SecretEnv names the environment variables that hold secrets in any
// factory process. Their values are redacted wherever they would appear.
var SecretEnv = []string{
	"SISF_STATE_DSN", "SISF_OWNER_TOKEN", "SISF_WEBHOOK_SECRET", "SISF_GITHUB_TOKEN", "SISF_APPROVAL_KEY",
	"SISF_DISPATCHER_ADMIN_TOKEN", "EDGE_ADMIN_TOKEN", "SISF_ROOT_PUBLIC_KEY",
}

// Process is one process's redacted logger and telemetry.
type Process struct {
	Log       *slog.Logger
	Telemetry *telemetry.OTel
	Redactor  *redact.Redactor
}

// Start builds the logger and the telemetry of a process that writes its
// log to w. Every value of the SecretEnv variables getenv returns, and
// every extra secret, is redacted as an exact value besides what the
// redactor recognises by shape.
func Start(w io.Writer, getenv func(string) string, clock core.Clock, extra ...string) (*Process, error) {
	var env []string
	for _, name := range SecretEnv {
		if v := getenv(name); strings.TrimSpace(v) != "" {
			env = append(env, name+"="+v)
		}
	}
	r := redact.New(redact.Config{Env: env, Secrets: extra})
	log := slog.New(redact.NewLogHandler(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}), r))
	tel, err := telemetry.NewOTel(telemetry.OTelConfig{Exporter: logExporter{log: log}, Redactor: r, Clock: clock})
	if err != nil {
		return nil, err
	}
	return &Process{Log: log, Telemetry: tel, Redactor: r}, nil
}

// Shutdown flushes the telemetry.
func (p *Process) Shutdown(ctx context.Context) error { return p.Telemetry.Shutdown(ctx) }

// logExporter writes one log record per finished span, through the
// redacting logger: a deployment without a collector still keeps its traces
// in its log, and a collector can be added without touching the processes.
type logExporter struct{ log *slog.Logger }

func (e logExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	for _, s := range spans {
		attrs := []any{"span", s.Name(), "duration", s.EndTime().Sub(s.StartTime()).String(), "status", s.Status().Code.String()}
		for _, kv := range s.Attributes() {
			attrs = append(attrs, string(kv.Key), kv.Value.String())
		}
		e.log.Info("telemetry span", attrs...)
	}
	return nil
}

func (logExporter) Shutdown(context.Context) error { return nil }
