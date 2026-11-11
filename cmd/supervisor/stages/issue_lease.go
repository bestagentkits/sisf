package stages

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// takeLease acquires the issue lease for owner.
func takeLease(ctx context.Context, store state.Store, issue core.IssueRef, owner string, ttl time.Duration) (state.Lease, error) {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	var lease state.Lease
	err := store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		lease, err = tx.AcquireLease(ctx, state.IssueLeaseResource(issue), owner, ttl)
		return err
	})
	return lease, err
}

// letGo gives an issue lease up; a lease already lost needs nothing more.
func letGo(ctx context.Context, store state.Store, lease state.Lease) {
	_ = store.Update(context.WithoutCancel(ctx), func(ctx context.Context, tx state.Tx) error {
		return tx.ReleaseLease(ctx, lease)
	})
}
