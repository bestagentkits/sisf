package handoff_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// Outage records of builds that are not held, such as those of issues that
// were given up on, can far outnumber the held ones; the listing of held
// builds must still show every held build.
func TestEveryHeldBuildIsListedWhateverTheOtherOutageRecords(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		store := db.Open(t, core.ZoneControl)
		now := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
		outage := func(run string, held bool) handoff.BuildOutage {
			return handoff.BuildOutage{RunID: core.RunID(run), IssueID: core.IssueRef{Owner: "acme", Repo: "app", Number: 1},
				Attempt: 1, Failures: 1, LastCode: core.CodeUnavailable, Held: held, UpdatedAt: now}
		}
		err := store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			for i := range 600 {
				if _, err := handoff.PutBuildOutage(ctx, tx, outage(fmt.Sprintf("run-a%04d", i), false), 0); err != nil {
					return err
				}
			}
			_, err := handoff.PutBuildOutage(ctx, tx, outage("run-zzzz", true), 0)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		held := listHeld(t, store)
		if len(held) != 1 || held[0].RunID != "run-zzzz" {
			t.Fatalf("held builds listed: %+v, want run-zzzz", held)
		}

		// The Owner's retry resumes the build; it is no longer listed.
		err = store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			return handoff.ResumeHeldBuild(ctx, tx, "run-zzzz", 1, "user:owner-1", now.Add(time.Hour))
		})
		if err != nil {
			t.Fatal(err)
		}
		if held := listHeld(t, store); len(held) != 0 {
			t.Fatalf("a resumed build is still listed as held: %+v", held)
		}

		// Held again and then built: its records go, the listing with them.
		err = store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			o, version, err := handoff.ReadBuildOutage(ctx, tx, "run-zzzz", 1)
			if err != nil {
				return err
			}
			o.Held = true
			if version, err = handoff.PutBuildOutage(ctx, tx, o, version); err != nil {
				return err
			}
			return handoff.DeleteBuildOutage(ctx, tx, "run-zzzz", 1, version)
		})
		if err != nil {
			t.Fatal(err)
		}
		if held := listHeld(t, store); len(held) != 0 {
			t.Fatalf("a build whose outage record is gone is still listed as held: %+v", held)
		}
	})
}

func listHeld(t *testing.T, store state.Store) []handoff.BuildOutage {
	t.Helper()
	var held []handoff.BuildOutage
	err := store.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		held, err = handoff.ListHeldBuilds(ctx, tx)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return held
}
