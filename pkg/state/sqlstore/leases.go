package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// A lease row outlives its holders: the token only grows, so a token is
// never issued twice for a resource. A released lease keeps its row with an
// empty holder.

// AcquireLease grants the resource when it is free, when its lease has
// expired, or when the same owner asks again; otherwise LEASE_HELD, whose
// details name the holder. Every
// grant carries a token larger than any before it, including a grant to the
// owner that already held the lease: a restarted holder thereby fences off
// whatever its earlier incarnation still has in flight.
func (t *tx) AcquireLease(ctx context.Context, resource, owner string, ttl time.Duration) (state.Lease, error) {
	if err := t.writes(tableLeases, canSelect|canInsert|canUpdate); err != nil {
		return state.Lease{}, err
	}
	if err := validLeaseRequest(resource, owner, ttl); err != nil {
		return state.Lease{}, err
	}
	now := t.store.now()
	lease := state.Lease{Resource: resource, Owner: owner, ExpiresAt: now.Add(ttl)}
	var token int64
	// One statement decides and writes, so two contenders cannot both win.
	err := t.q.row(ctx, `INSERT INTO leases (factory_id, resource, holder, token, expires_at)
		VALUES (?, ?, ?, 1, ?)
		ON CONFLICT (factory_id, resource) DO UPDATE
		SET holder = excluded.holder, token = leases.token + 1, expires_at = excluded.expires_at
		WHERE leases.holder = excluded.holder OR leases.holder = '' OR leases.expires_at <= ?
		RETURNING token`,
		string(t.store.factory), resource, owner, toNanos(lease.ExpiresAt), toNanos(now)).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		held := core.NewError(core.CodeLeaseHeld, "another owner holds the lease on "+resource)
		// The holder is named so a caller that must wait can say on whom.
		if holder, _, found, readErr := t.leaseRow(ctx, resource, false); readErr == nil && found {
			held.Details = map[string]string{"holder": holder}
		}
		return state.Lease{}, held
	}
	if err != nil {
		return state.Lease{}, t.store.dialect.failure("acquire lease", err)
	}
	lease.Token = core.FencingToken(token)
	return lease, nil
}

// RenewLease extends a lease that is still held and keeps its token. A lease
// taken over or given up returns STALE_FENCING_TOKEN. A lease that lapsed
// without a successor returns EXPIRED: it is no longer held, and its owner
// must acquire again, under a new token.
func (t *tx) RenewLease(ctx context.Context, lease state.Lease, ttl time.Duration) (state.Lease, error) {
	if err := t.writes(tableLeases, canSelect|canUpdate); err != nil {
		return state.Lease{}, err
	}
	if err := validLeaseRequest(lease.Resource, lease.Owner, ttl); err != nil {
		return state.Lease{}, err
	}
	now := t.store.now()
	renewed := lease
	renewed.ExpiresAt = now.Add(ttl)
	changed, err := t.q.exec(ctx, "renew lease", `UPDATE leases SET expires_at = ?
		WHERE factory_id = ? AND resource = ? AND holder = ? AND token = ? AND expires_at > ?`,
		toNanos(renewed.ExpiresAt), string(t.store.factory), lease.Resource, lease.Owner, int64(lease.Token), toNanos(now))
	if err != nil {
		return state.Lease{}, err
	}
	if changed == 1 {
		return renewed, nil
	}
	holder, token, found, err := t.leaseRow(ctx, lease.Resource, false)
	if err != nil {
		return state.Lease{}, err
	}
	if !found || holder != lease.Owner || token != lease.Token {
		return state.Lease{}, staleToken(lease.Resource)
	}
	return state.Lease{}, core.NewError(core.CodeExpired, "the lease on "+lease.Resource+" has lapsed; acquire it again")
}

// ReleaseLease gives the resource up before its lease expires. Releasing
// twice is harmless. A holder that was taken over gets STALE_FENCING_TOKEN
// and leaves the lease of its successor untouched.
func (t *tx) ReleaseLease(ctx context.Context, lease state.Lease) error {
	if err := t.writes(tableLeases, canSelect|canUpdate); err != nil {
		return err
	}
	if err := validLeaseRequest(lease.Resource, lease.Owner, time.Nanosecond); err != nil {
		return err
	}
	changed, err := t.q.exec(ctx, "release lease", `UPDATE leases SET holder = '', expires_at = 0
		WHERE factory_id = ? AND resource = ? AND holder = ? AND token = ?`,
		string(t.store.factory), lease.Resource, lease.Owner, int64(lease.Token))
	if err != nil || changed == 1 {
		return err
	}
	holder, token, found, err := t.leaseRow(ctx, lease.Resource, false)
	if err != nil {
		return err
	}
	if found && holder == "" && token == lease.Token {
		return nil // already released
	}
	return staleToken(lease.Resource)
}

// CheckFencing returns STALE_FENCING_TOKEN unless token is the latest one
// issued for resource. In a read-write transaction the lease row stays
// locked until commit, so a successor cannot be granted the lease between
// this check and the write it protects.
func (t *tx) CheckFencing(ctx context.Context, resource string, token core.FencingToken) error {
	if err := t.reads(tableLeases); err != nil {
		return err
	}
	_, latest, found, err := t.leaseRow(ctx, resource, !t.readOnly)
	if err != nil {
		return err
	}
	if !found || token <= 0 || token != latest {
		return staleToken(resource)
	}
	return nil
}

// leaseRow reads the holder and latest token of a resource. With lock set,
// the row cannot change before the transaction ends.
func (t *tx) leaseRow(ctx context.Context, resource string, lock bool) (holder string, token core.FencingToken, found bool, err error) {
	query := `SELECT holder, token FROM leases WHERE factory_id = ? AND resource = ?`
	if lock {
		query += t.store.dialect.shareLock()
	}
	var raw int64
	err = t.q.row(ctx, query, string(t.store.factory), resource).Scan(&holder, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, t.store.dialect.failure("read lease", err)
	}
	return holder, core.FencingToken(raw), true, nil
}

func validLeaseRequest(resource, owner string, ttl time.Duration) error {
	if err := requireText("lease resource", resource); err != nil {
		return err
	}
	if err := requireText("lease owner", owner); err != nil {
		return err
	}
	if ttl <= 0 {
		return invalid("a lease needs a positive lifetime, got %s", ttl)
	}
	return nil
}

func staleToken(resource string) *core.Error {
	return core.NewError(core.CodeStaleFencingToken, "the fencing token is not the latest one issued for "+resource)
}
