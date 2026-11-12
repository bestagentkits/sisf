package control_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// A stored report that cannot be read is given up at once, and what named
// it goes with it in the same write: its client no longer counts it as
// waiting, and its fingerprint no longer points at it, so the same report
// sent again is accepted as a new one rather than failing on a report
// that is gone.
func TestAnUnreadableReportIsGivenUpWithoutLeavingItsIndexesBehind(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e, gh, p := reportEnv(t, db)
		if code, _ := sendReportText(t, e, "Export hangs", "Body.", 3); code != http.StatusAccepted {
			t.Fatalf("the report: %d", code)
		}
		err := e.store.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
			entries, err := tx.ListKV(ctx, control.ReportsNamespace, "r/", 0)
			if err != nil || len(entries) != 1 {
				t.Fatalf("waiting reports %v: %v", entries, err)
			}
			entries[0].Value = []byte("{not json")
			_, err = tx.PutKV(ctx, entries[0])
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if res := publish(t, p); res.Published != 0 || countKeys(t, e, "f/") != 1 || countKeys(t, e, "r/") != 0 {
			t.Fatalf("publishing an unreadable report: %+v, want it given up and kept apart", res)
		}
		if n := countKeys(t, e, "c/"); n != 0 {
			t.Fatalf("%d client entries left after the only waiting report was given up, want none", n)
		}
		if n := countKeys(t, e, "fp/"); n != 0 {
			t.Fatalf("%d fingerprint entries left pointing at an unreadable report, want none", n)
		}
		if code, status := sendReportText(t, e, "Export hangs", "Body.", 3); code != http.StatusAccepted || status != "accepted" {
			t.Fatalf("the same report sent again: %d %q, want it accepted as new", code, status)
		}
		publish(t, p)
		if issues := gh.created(); len(issues) != 1 || issues[0].Title != "Report: Export hangs" {
			t.Fatalf("issues %+v, want the report sent again published", issues)
		}
	})
}
