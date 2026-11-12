package sqlstore_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// acquire takes a lease in a transaction of its own.
func acquire(s *sqlstore.Store, resource, owner string, ttl time.Duration) (state.Lease, error) {
	var lease state.Lease
	err := update(s, func(tx state.Tx) (err error) {
		lease, err = tx.AcquireLease(ctx, resource, owner, ttl)
		return err
	})
	return lease, err
}

// fencing checks a token in a transaction of its own.
func fencing(s *sqlstore.Store, resource string, token core.FencingToken) error {
	return view(s, func(tx state.Tx) error { return tx.CheckFencing(ctx, resource, token) })
}

func TestLeaseIsExclusiveUntilItExpiresAndTokensOnlyGrow(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneAgent)
		first, err := acquire(s, "dispatcher", "dispatcher-1", time.Minute)
		if err != nil || first.Token != 1 || first.Owner != "dispatcher-1" || first.Resource != "dispatcher" {
			t.Fatalf("first lease = %+v, %v", first, err)
		}
		if want := db.Clock.Now().Add(time.Minute); !first.ExpiresAt.Equal(want) {
			t.Fatalf("ExpiresAt = %s, want %s", first.ExpiresAt, want)
		}

		// Another owner waits while the lease is held, up to the last instant.
		db.Clock.Advance(time.Minute - time.Nanosecond)
		_, err = acquire(s, "dispatcher", "dispatcher-2", time.Minute)
		wantCode(t, err, core.CodeLeaseHeld)

		// At expiry the lease is free and the next holder gets a larger token.
		db.Clock.Advance(time.Nanosecond)
		second, err := acquire(s, "dispatcher", "dispatcher-2", time.Minute)
		if err != nil || second.Token <= first.Token {
			t.Fatalf("second lease = %+v, %v; its token must exceed %d", second, err, first.Token)
		}

		// Leases on other resources are independent.
		other, err := acquire(s, "supervisor", "supervisor-1", time.Minute)
		if err != nil || other.Token != 1 {
			t.Fatalf("lease on another resource = %+v, %v", other, err)
		}
	})
}

func TestSupersededHolderIsFencedOff(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneAgent)
		old, _ := acquire(s, "dispatcher", "dispatcher-1", time.Minute)
		if err := fencing(s, "dispatcher", old.Token); err != nil {
			t.Fatalf("the current holder must pass: %v", err)
		}
		db.Clock.Advance(time.Hour)
		current, err := acquire(s, "dispatcher", "dispatcher-2", time.Minute)
		if err != nil {
			t.Fatal(err)
		}

		wantCode(t, fencing(s, "dispatcher", old.Token), core.CodeStaleFencingToken)
		if err := fencing(s, "dispatcher", current.Token); err != nil {
			t.Fatalf("the latest holder must pass: %v", err)
		}
		// A token that was never issued, no token, and an unknown resource all fail.
		wantCode(t, fencing(s, "dispatcher", current.Token+1), core.CodeStaleFencingToken)
		wantCode(t, fencing(s, "dispatcher", 0), core.CodeStaleFencingToken)
		wantCode(t, fencing(s, "never-leased", 1), core.CodeStaleFencingToken)

		// The old holder can neither extend nor give up a lease it lost.
		err = update(s, func(tx state.Tx) error {
			_, err := tx.RenewLease(ctx, old, time.Minute)
			return err
		})
		wantCode(t, err, core.CodeStaleFencingToken)
		wantCode(t, update(s, func(tx state.Tx) error { return tx.ReleaseLease(ctx, old) }), core.CodeStaleFencingToken)
		_, err = acquire(s, "dispatcher", "dispatcher-3", time.Minute)
		wantCode(t, err, core.CodeLeaseHeld)
	})
}

func TestRenewKeepsTheTokenAndALapsedLeaseCannotBeRenewed(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneAgent)
		lease, _ := acquire(s, "dispatcher", "dispatcher-1", time.Minute)
		db.Clock.Advance(30 * time.Second)

		var renewed state.Lease
		mustUpdate(t, s, func(tx state.Tx) (err error) {
			renewed, err = tx.RenewLease(ctx, lease, time.Minute)
			return err
		})
		if renewed.Token != lease.Token || !renewed.ExpiresAt.Equal(db.Clock.Now().Add(time.Minute)) {
			t.Fatalf("renewed lease = %+v", renewed)
		}
		db.Clock.Advance(45 * time.Second)
		_, err := acquire(s, "dispatcher", "dispatcher-2", time.Minute)
		wantCode(t, err, core.CodeLeaseHeld)

		// Once the lease has lapsed the holder must acquire again.
		db.Clock.Advance(time.Minute)
		err = update(s, func(tx state.Tx) error {
			_, err := tx.RenewLease(ctx, renewed, time.Minute)
			return err
		})
		wantCode(t, err, core.CodeExpired)
	})
}

func TestReleasedLeaseIsFreeAndItsTokenIsNeverReused(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneAgent)
		first, _ := acquire(s, "dispatcher", "dispatcher-1", time.Hour)
		mustUpdate(t, s, func(tx state.Tx) error { return tx.ReleaseLease(ctx, first) })
		// Giving up a lease twice is harmless.
		mustUpdate(t, s, func(tx state.Tx) error { return tx.ReleaseLease(ctx, first) })

		second, err := acquire(s, "dispatcher", "dispatcher-2", time.Hour)
		if err != nil || second.Token <= first.Token {
			t.Fatalf("lease after release = %+v, %v; its token must exceed %d", second, err, first.Token)
		}

		// An owner that acquires again while holding fences off its earlier self.
		again, err := acquire(s, "dispatcher", "dispatcher-2", time.Hour)
		if err != nil || again.Token <= second.Token {
			t.Fatalf("lease after a restart = %+v, %v; its token must exceed %d", again, err, second.Token)
		}
		wantCode(t, fencing(s, "dispatcher", second.Token), core.CodeStaleFencingToken)
	})
}

func TestMalformedLeaseRequestsAreRefused(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneAgent)
		for name, try := range map[string]func() (state.Lease, error){
			"no resource":  func() (state.Lease, error) { return acquire(s, "", "dispatcher-1", time.Minute) },
			"no owner":     func() (state.Lease, error) { return acquire(s, "dispatcher", "", time.Minute) },
			"no lifetime":  func() (state.Lease, error) { return acquire(s, "dispatcher", "dispatcher-1", 0) },
			"negative ttl": func() (state.Lease, error) { return acquire(s, "dispatcher", "dispatcher-1", -time.Second) },
		} {
			if _, err := try(); !core.HasCode(err, core.CodeInvalidArgument) {
				t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, err)
			}
		}
	})
}

func TestConcurrentAcquirersGetOneLease(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		a, b := db.Open(t, core.ZoneAgent), db.Open(t, core.ZoneControl)
		const contenders = 8
		var wg sync.WaitGroup
		errs := make([]error, contenders)
		for i := range contenders {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s := a
				if i%2 == 1 {
					s = b
				}
				_, errs[i] = acquire(s, "dispatcher", "dispatcher-"+string(rune('a'+i)), time.Minute)
			}()
		}
		wg.Wait()
		held := 0
		for _, err := range errs {
			switch {
			case err == nil:
				held++
			case !core.HasCode(err, core.CodeLeaseHeld):
				t.Fatalf("a contender that lost must see LEASE_HELD, got %v", err)
			}
		}
		if held != 1 {
			t.Fatalf("%d contenders hold the lease, want exactly 1", held)
		}
	})
}

// A refused lease names who holds it, so a caller that must wait can say
// on whom.
func TestARefusedLeaseNamesItsHolder(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		if _, err := acquire(s, "issue:acme/hello#1", "killswitch", time.Minute); err != nil {
			t.Fatal(err)
		}
		_, err := acquire(s, "issue:acme/hello#1", "supervisor", time.Minute)
		var refused *core.Error
		if !errors.As(err, &refused) || refused.Code != core.CodeLeaseHeld || refused.Details["holder"] != "killswitch" {
			t.Fatalf("a refused lease = %v (details %v), want LEASE_HELD naming the holder", err, refused)
		}
	})
}
