package sqlstore_test

import (
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// A run has usage without a reported cost when the ledger holds unverified
// usage of that run with tokens and no cost, however long ago it was
// recorded. Usage with a cost, verified usage, usage without tokens and
// usage of another run do not count. Both zones that close a run's
// reservation can ask.
func TestRunCostUnreportedReadsTheLedgerOfThatRun(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control := db.Open(t, core.ZoneControl)
		now := db.Clock.Now()
		forRun := func(run core.RunID, u core.Usage) core.Usage {
			u.RunID, u.ReservationID = run, ""
			return u
		}
		noTokens := forRun("run-priced", spend(0, false, now))
		noTokens.InputTokens, noTokens.OutputTokens = 0, 0
		huge := forRun("run-huge", spend(0, false, now))
		huge.InputTokens, huge.OutputTokens = 1<<62, 1<<62
		mustUpdate(t, control, func(tx state.Tx) error {
			for _, u := range []core.Usage{
				forRun("run-old", spend(0, false, now.Add(-24*365*time.Hour))),
				forRun("run-priced", spend(60, false, now)),
				forRun("run-priced", spend(0, true, now)),
				noTokens,
				huge,
			} {
				if err := tx.AppendUsage(ctx, u); err != nil {
					return err
				}
			}
			return nil
		})
		for _, zone := range []core.Zone{core.ZoneControl, core.ZoneAgent} {
			s := db.Open(t, zone)
			for run, want := range map[core.RunID]bool{"run-old": true, "run-huge": true, "run-priced": false, "run-none": false} {
				var got bool
				if err := view(s, func(tx state.Tx) (err error) {
					got, err = tx.RunCostUnreported(ctx, run)
					return err
				}); err != nil {
					t.Fatalf("%s zone, run %s: %v", zone, run, err)
				}
				if got != want {
					t.Errorf("%s zone: RunCostUnreported(%s) = %v, want %v", zone, run, got, want)
				}
			}
		}
	})
}
