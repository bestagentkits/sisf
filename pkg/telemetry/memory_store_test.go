package telemetry_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/telemetry"
)

func storedEvent(kind telemetry.Kind, name string, at time.Time, run core.RunID) telemetry.Event {
	attrs := runAttributes()
	attrs.RunID = run
	return telemetry.Event{Kind: kind, Name: name, At: at, Attributes: attrs, Fields: map[string]string{"k": "v"}}
}

func TestMemoryStoreFiltersByEveryQueryField(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	store := telemetry.NewMemoryStore(10)
	for i, e := range []telemetry.Event{
		storedEvent(telemetry.KindLog, "a", base, "run-1"),
		storedEvent(telemetry.KindMetric, "b", base.Add(time.Hour), "run-1"),
		storedEvent(telemetry.KindLog, "a", base.Add(2*time.Hour), "run-2"),
	} {
		if err := store.Append(ctx, e); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	count := func(q telemetry.Query) int {
		got, err := store.Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	checks := map[string]struct {
		q    telemetry.Query
		want int
	}{
		"all":     {telemetry.Query{}, 3},
		"kind":    {telemetry.Query{Kind: telemetry.KindLog}, 2},
		"name":    {telemetry.Query{Name: "b"}, 1},
		"run":     {telemetry.Query{RunID: "run-2"}, 1},
		"issue":   {telemetry.Query{IssueRef: core.IssueRef{Owner: "acme", Repo: "app", Number: 8}}, 0},
		"release": {telemetry.Query{ReleaseDigest: digest}, 3},
		"since":   {telemetry.Query{Since: base.Add(time.Hour)}, 2},
		"until":   {telemetry.Query{Until: base.Add(time.Hour)}, 1},
		"limit":   {telemetry.Query{Limit: 2}, 2},
	}
	for name, c := range checks {
		if got := count(c.q); got != c.want {
			t.Errorf("%s: got %d, want %d", name, got, c.want)
		}
	}
	if _, err := store.Query(ctx, telemetry.Query{Limit: -1}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("a negative limit is refused, got %v", err)
	}
}

func TestMemoryStoreReturnsCopiesAndStaysBounded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	store := telemetry.NewMemoryStore(2)
	for i := range 3 {
		if err := store.Append(ctx, storedEvent(telemetry.KindLog, "e", base.Add(time.Duration(i)*time.Minute), "run-1")); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := store.Query(ctx, telemetry.Query{})
	if len(got) != 2 || !got[0].At.Equal(base.Add(time.Minute)) {
		t.Fatalf("the oldest event is dropped first: %+v", got)
	}
	got[0].Fields["k"] = "changed"
	again, _ := store.Query(ctx, telemetry.Query{})
	if again[0].Fields["k"] != "v" {
		t.Error("a reader must not be able to change stored events")
	}
}

func TestMemoryStoreDeletesExpiredEventsOfOneKind(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	store := telemetry.NewMemoryStore(0)
	_ = store.Append(ctx, storedEvent(telemetry.KindLog, "old", base, "run-1"))
	_ = store.Append(ctx, storedEvent(telemetry.KindMetric, "old", base, "run-1"))
	_ = store.Append(ctx, storedEvent(telemetry.KindLog, "new", base.Add(time.Hour), "run-1"))
	if n := store.DeleteBefore(telemetry.KindLog, base.Add(time.Minute)); n != 1 {
		t.Fatalf("deleted %d, want 1", n)
	}
	got, _ := store.Query(ctx, telemetry.Query{})
	if len(got) != 2 {
		t.Fatalf("only the expired log may go: %+v", got)
	}
}
