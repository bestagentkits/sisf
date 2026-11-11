package sqlstore_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// Every lease operation the store makes still goes through for each zone
// that holds leases, over the zone's own database role: taking a free
// lease, taking it again as its holder, renewing it, giving it up twice,
// and taking over a lease that expired.
func TestPostgresEveryZoneTakesRenewsAndGivesUpLeasesThroughItsOwnRole(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		for _, zone := range []core.Zone{core.ZoneAgent, core.ZoneControl} {
			s := db.OpenWith(t, logins.DSN[zone], zone)
			resource := "lease-ops-" + string(zone)
			first, err := acquire(s, resource, "holder-a", time.Minute)
			if err != nil || first.Token != 1 {
				t.Fatalf("%s: take a free lease: %+v, %v", zone, first, err)
			}
			again, err := acquire(s, resource, "holder-a", time.Minute)
			if err != nil || again.Token != 2 {
				t.Fatalf("%s: take the lease again as its holder: %+v, %v", zone, again, err)
			}
			if _, err := acquire(s, resource, "holder-b", time.Minute); !core.HasCode(err, core.CodeLeaseHeld) {
				t.Fatalf("%s: a live lease went to another holder: %v", zone, err)
			}
			var renewed state.Lease
			mustUpdate(t, s, func(tx state.Tx) (err error) {
				renewed, err = tx.RenewLease(ctx, again, 2*time.Minute)
				return err
			})
			if renewed.Token != again.Token {
				t.Fatalf("%s: the renewal changed the token to %d", zone, renewed.Token)
			}
			for range 2 {
				mustUpdate(t, s, func(tx state.Tx) error { return tx.ReleaseLease(ctx, renewed) })
			}
			freed, err := acquire(s, resource, "holder-b", time.Minute)
			if err != nil || freed.Token != 3 {
				t.Fatalf("%s: take a given-up lease: %+v, %v", zone, freed, err)
			}
			db.Clock.Advance(2 * time.Minute)
			takenOver, err := acquire(s, resource, "holder-c", time.Minute)
			if err != nil || takenOver.Token != 4 {
				t.Fatalf("%s: take over an expired lease: %+v, %v", zone, takenOver, err)
			}
			if err := fencing(s, resource, takenOver.Token); err != nil {
				t.Fatalf("%s: the latest token was refused: %v", zone, err)
			}
		}
	})
}

// A stolen agent-zone credential used with plain SQL cannot set a lease's
// token to what it likes, so it cannot meet the record trigger's rule that
// a step carries the latest token of the issue lease without taking the
// lease, which names it as the holder and fences off the one before.
func TestPostgresAZoneRoleCannotForgeALeaseToken(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		owner := db.Open(t, core.ZoneControl)
		agent := rawLogin(t, logins.DSN[core.ZoneAgent])
		candidate := releaseCandidate(t, owner, issue(140))
		resource := state.IssueLeaseResource(candidate.IssueID)
		where := ` WHERE factory_id = '` + string(sqlstoretest.Factory) + `' AND resource = '` + resource + `'`
		for name, statement := range map[string]string{
			"a token raised past the next one":    `UPDATE leases SET token = token + 7` + where,
			"a token raised with no holder":       `UPDATE leases SET token = token + 1, holder = ''` + where,
			"a token lowered":                     `UPDATE leases SET token = token - 1` + where,
			"a lease moved to another resource":   `UPDATE leases SET resource = 'issue:acme/app#999'` + where,
			"a lease given up but its token kept": `UPDATE leases SET holder = '', expires_at = 0, token = token + 1` + where,
			"a renewal that changes the holder":   `UPDATE leases SET holder = 'agent-thief'` + where,
			"a new lease that starts at a high token": fmt.Sprintf(`INSERT INTO leases (factory_id, resource, holder, token, expires_at)
				VALUES ('%s', 'issue:acme/app#998', 'agent-thief', 9, 1)`, sqlstoretest.Factory),
		} {
			refusedRow(t, agent, name, statement)
		}
		// The record's lease is untouched, so a step under a forged token
		// is still refused by the store and the record trigger.
		refused(t, db.OpenWith(t, logins.DSN[core.ZoneControl], core.ZoneControl), "a step under a token that was never issued",
			next(t, candidate, core.EventReleaseRefused, func(tr *core.Transition) {
				tr.FencingToken += 7
				tr.IdempotencyKey = "release/run-1/release_refused"
			}), core.CodeStaleFencingToken)
	})
}
