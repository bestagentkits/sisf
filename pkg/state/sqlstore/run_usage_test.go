package sqlstore_test

import (
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// RunUsage asked from the beginning of time sums every cost booked for one
// run, counts its entries and says whether some of them counted tokens
// without a cost. Usage of another run is left out, usage without tokens
// and without a cost is not cost-unreported, and token counts too large to
// add up in the database do not break the read. Both zones that close a
// run's reservation can ask.
func TestRunUsageSumsTheLedgerOfThatRun(t *testing.T) {
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
				forRun("run-old", spend(40, false, now)),
				forRun("run-priced", spend(60, false, now)),
				forRun("run-priced", spend(25, false, now)),
				noTokens,
				huge,
			} {
				if err := tx.AppendUsage(ctx, u); err != nil {
					return err
				}
			}
			return nil
		})
		want := map[core.RunID]core.RunUsage{
			"run-old":    {Cost: 40, CostUnreported: true, Entries: 2},
			"run-huge":   {Cost: 0, CostUnreported: true, Entries: 1},
			"run-priced": {Cost: 85, CostUnreported: false, Entries: 3},
			"run-none":   {},
		}
		for _, zone := range []core.Zone{core.ZoneControl, core.ZoneAgent} {
			s := db.Open(t, zone)
			for run, w := range want {
				var got core.RunUsage
				if err := view(s, func(tx state.Tx) (err error) {
					got, err = tx.RunUsage(ctx, run, time.Time{})
					return err
				}); err != nil {
					t.Fatalf("%s zone, run %s: %v", zone, run, err)
				}
				if got != w {
					t.Errorf("%s zone: RunUsage(%s) = %+v, want %+v", zone, run, got, w)
				}
			}
		}
	})
}
