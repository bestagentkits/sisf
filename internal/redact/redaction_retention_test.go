package redact_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/redact"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/telemetry"
)

// sensitive holds one sample of each kind of data that must never reach a
// log, a transcript, an issue or a pull request.
var sensitive = map[string]string{
	"provider token":  "sk-ant-api03-abcdefghijklmnop",
	"github token":    "ghp_abcdefghijklmnopqrstuvwxyz0123",
	"run secret":      "opaque-run-secret-0042",
	"email":           "jane.doe@example.org",
	"phone":           "+84 912 345 678",
	"ip address":      "203.0.113.42",
	"payment card":    "4111 1111 1111 1111",
	"assigned secret": "DB_PASSWORD=correct-horse-battery",
}

func newRedactor() *redact.Redactor {
	return redact.New(redact.Config{Env: []string{"RUN_TOKEN=opaque-run-secret-0042", "HOME=/home/agent"}})
}

func leaks(t *testing.T, where, out string) {
	t.Helper()
	for kind, value := range sensitive {
		probe := value
		if kind == "assigned secret" {
			probe = "correct-horse-battery"
		}
		if strings.Contains(out, probe) {
			t.Errorf("%s leaked the %s: %q", where, kind, out)
		}
	}
}

func TestSecretsAndPersonalDataAreRedactedBeforeEveryDestination(t *testing.T) {
	t.Parallel()
	r := newRedactor()
	var _ telemetry.Redactor = r
	var parts []string
	for _, v := range sensitive {
		parts = append(parts, v)
	}
	text := "context: " + strings.Join(parts, " | ") + " /home/agent"

	// Transcripts, issue bodies and pull-request bodies all go through Redact.
	for _, where := range []string{"transcript", "issue body", "pull request body"} {
		leaks(t, where, r.Redact(text))
	}
	if !strings.Contains(r.Redact(text), "/home/agent") {
		t.Error("a non-secret value was redacted")
	}
	fields := r.Fields(map[string]string{"contact": "jane.doe@example.org", "status": "ok"})
	leaks(t, "fields", fields["contact"])
	if fields["status"] != "ok" {
		t.Errorf("plain fields must pass unchanged, got %q", fields["status"])
	}

	// Logs: the message, plain attributes, grouped attributes and errors.
	var buf bytes.Buffer
	logger := slog.New(redact.NewLogHandler(slog.NewJSONHandler(&buf, nil), r))
	logger.With("token", "ghp_abcdefghijklmnopqrstuvwxyz0123").WithGroup("req").
		Info("user "+sensitive["email"], "ip", sensitive["ip address"], "err", errors.New(sensitive["run secret"]),
			slog.Group("card", "number", sensitive["payment card"]))
	leaks(t, "log line", buf.String())
	if !strings.Contains(buf.String(), redact.Marker) {
		t.Errorf("the log line must show where data was removed: %s", buf.String())
	}
}

func TestOrdinaryOperationalTextIsNotMistakenForPersonalData(t *testing.T) {
	t.Parallel()
	r := newRedactor()
	for _, plain := range []string{
		"release sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"at 2026-11-10T08:00:00Z run-0001 took 1234 ms",
		"GET /api/v1/issues/42 returned 503 for 17 of 900 requests",
		"card 4111 1111 1111 1112 fails the checksum",
		"version 300.1.2.3 is not an address",
	} {
		if got := r.Redact(plain); got != plain {
			t.Errorf("plain text changed:\n got %q\nwant %q", got, plain)
		}
	}
}

func TestEveryDataKindHasARetentionPeriod(t *testing.T) {
	t.Parallel()
	policy := redact.DefaultRetention()
	if err := policy.Validate(); err != nil {
		t.Fatalf("default retention must be complete: %v", err)
	}
	for _, kind := range redact.DataKinds() {
		if policy[kind] <= 0 {
			t.Errorf("kind %s has no retention period", kind)
		}
	}
	incomplete := redact.DefaultRetention()
	delete(incomplete, redact.KindTranscript)
	zero := redact.DefaultRetention()
	zero[redact.KindLog] = 0
	unknown := redact.DefaultRetention()
	unknown["screenshots"] = time.Hour
	for name, p := range map[string]redact.Retention{"missing kind": incomplete, "zero period": zero, "unknown kind": unknown} {
		if !core.HasCode(p.Validate(), core.CodeInvalidArgument) {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, p.Validate())
		}
	}
}

func TestSweepPurgesEachKindAfterItsOwnPeriod(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	clock := &stepClock{now: now}
	store := telemetry.NewMemoryStore(0)
	add := func(kind telemetry.Kind, age time.Duration) {
		e := telemetry.Event{Kind: kind, Name: "e", At: now.Add(-age)}
		if err := store.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	policy := redact.DefaultRetention()
	add(telemetry.KindLog, policy[redact.KindLog]+time.Hour)       // expired
	add(telemetry.KindLog, policy[redact.KindLog]-time.Hour)       // kept
	add(telemetry.KindMetric, policy[redact.KindLog]+time.Hour)    // metrics live longer: kept
	add(telemetry.KindMetric, policy[redact.KindMetric]+time.Hour) // expired
	purge := func(kind telemetry.Kind) redact.PurgeFunc {
		return func(_ context.Context, before time.Time) (int, error) { return store.DeleteBefore(kind, before), nil }
	}
	sweeper, err := redact.NewSweeper(policy, clock, map[redact.DataKind]redact.PurgeFunc{
		redact.KindLog: purge(telemetry.KindLog), redact.KindMetric: purge(telemetry.KindMetric),
	})
	if err != nil {
		t.Fatal(err)
	}
	removed, err := sweeper.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if removed[redact.KindLog] != 1 || removed[redact.KindMetric] != 1 {
		t.Fatalf("removed = %v", removed)
	}
	if left, _ := store.Query(ctx, telemetry.Query{}); len(left) != 2 {
		t.Fatalf("two events are still within their period, got %d", len(left))
	}
}

func TestSweeperRefusesAnIncompletePolicyAndReportsPurgeFailures(t *testing.T) {
	t.Parallel()
	clock := &stepClock{now: time.Now()}
	ok := func(context.Context, time.Time) (int, error) { return 0, nil }
	bad := redact.DefaultRetention()
	delete(bad, redact.KindReport)
	if _, err := redact.NewSweeper(bad, clock, map[redact.DataKind]redact.PurgeFunc{redact.KindLog: ok}); err == nil {
		t.Error("a policy without every kind must be refused")
	}
	if _, err := redact.NewSweeper(redact.DefaultRetention(), clock, map[redact.DataKind]redact.PurgeFunc{"screenshots": ok}); err == nil {
		t.Error("a purger for a kind without a period must be refused")
	}
	if _, err := redact.NewSweeper(redact.DefaultRetention(), clock, map[redact.DataKind]redact.PurgeFunc{redact.KindLog: nil}); err == nil {
		t.Error("a nil purger must be refused")
	}
	failing := func(context.Context, time.Time) (int, error) { return 0, errors.New("disk gone") }
	s, err := redact.NewSweeper(redact.DefaultRetention(), clock, map[redact.DataKind]redact.PurgeFunc{redact.KindLog: failing, redact.KindReport: ok})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sweep(context.Background()); !core.HasCode(err, core.CodeUnavailable) {
		t.Errorf("a failed purge must be reported, got %v", err)
	}
}

type stepClock struct{ now time.Time }

func (c *stepClock) Now() time.Time { return c.now }
func (c *stepClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.now = c.now.Add(d)
	ch <- c.now
	return ch
}

func TestExpiryIsPerKindAndAGapInThePolicyDeletes(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	p := redact.DefaultRetention()
	if p.Expired(redact.KindLog, now.Add(-p[redact.KindLog]+time.Second), now) {
		t.Error("a log inside its period must be kept")
	}
	if !p.Expired(redact.KindLog, now.Add(-p[redact.KindLog]), now) {
		t.Error("a log at the end of its period must go")
	}
	if p.Expired(redact.KindMetric, now.Add(-p[redact.KindLog]), now) {
		t.Error("a metric of the same age is still inside its own, longer period")
	}
	if !p.Expired("screenshots", now, now) {
		t.Error("a kind the policy does not cover must not be kept")
	}
}
