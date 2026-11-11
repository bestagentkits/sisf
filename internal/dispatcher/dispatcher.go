package dispatcher

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// LeaseResource is the lease only the active dispatcher holds.
const LeaseResource = "dispatcher"

// Dispatcher claims Ready issues and drives their runs through the agent
// backend. It is safe for concurrent use; ticks are serialized.
type Dispatcher struct {
	cfg Config

	mu sync.Mutex
	// leader is the dispatcher lease while this instance holds it.
	leader *state.Lease
	// runs are the runs this instance holds the issue lease of.
	runs   map[core.RunID]*activeRun
	health Health
}

// activeRun is a run this instance holds the issue lease of.
type activeRun struct {
	issue core.IssueRef
	run   core.RunID
	lease state.Lease
	// unreachableSince is when the agent backend began answering the
	// current attempt's status calls with UNAVAILABLE; zero while it
	// answers. It lives only in this instance: a successor that takes the
	// run over starts its own count, still bounded by the run timeout.
	unreachableSince time.Time
}

// New builds a dispatcher. It touches neither the store nor the backend.
func New(cfg Config) (*Dispatcher, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	return &Dispatcher{cfg: cfg, runs: map[core.RunID]*activeRun{}, health: Health{Owner: cfg.Owner}}, nil
}

// TickResult says what one tick did.
type TickResult struct {
	// Leader is true when this instance held the dispatcher lease and worked.
	Leader bool
	// Claimed, Resumed, Succeeded and Exhausted list issues whose run this
	// tick claimed, took over from the stored state, finished, or stopped
	// after its last allowed attempt.
	Claimed   []core.IssueRef
	Resumed   []core.IssueRef
	Succeeded []core.IssueRef
	Exhausted []core.IssueRef
	// Lost lists issues whose lease this instance found taken over or lapsed.
	Lost []core.IssueRef
	// Failed lists the agent attempts this tick counted as failed, including
	// those that never started.
	Failed []FailedAttempt
	// Held is the typed reason Ready issues were left unclaimed, such as
	// BUDGET_UNCONFIRMED or LIMIT_EXCEEDED; nil when none waited for it.
	Held error
	// Errors are failures of single runs or issues; the tick went on past them.
	Errors []error
}

// Tick does one round of work: it takes or renews the dispatcher lease and,
// as the active dispatcher, takes over runs nobody holds, claims Ready
// issues within the limits, and moves every run it holds one step on. The
// error is a failure to reach the store; everything else is in the result.
func (d *Dispatcher) Tick(ctx context.Context) (TickResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var res TickResult
	err := d.lead(ctx, &res)
	if err == nil && res.Leader {
		d.resume(ctx, &res)
		d.claim(ctx, &res)
		d.supervise(ctx, &res)
	}
	d.record(res, err)
	return res, err
}

// FailedAttempt is one agent attempt of a run that did not succeed.
type FailedAttempt struct {
	Issue core.IssueRef
	RunID core.RunID
	AttemptFailure
}

// Run ticks until ctx ends, pausing PollInterval on the clock between ticks,
// and hands every tick's outcome to Config.OnTick. On the way out it gives
// up its leases so a successor need not wait for them to lapse, and
// returns nil.
func (d *Dispatcher) Run(ctx context.Context) error {
	for {
		res, err := d.Tick(ctx)
		if d.cfg.OnTick != nil {
			d.cfg.OnTick(res, err)
		}
		select {
		case <-ctx.Done():
			d.stop(context.WithoutCancel(ctx))
			return nil
		case <-d.cfg.Clock.After(d.cfg.PollInterval):
		}
	}
}

// stop releases the leases this instance holds. A lease already taken over
// is left to its new holder.
func (d *Dispatcher) stop(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	leases := make([]state.Lease, 0, len(d.runs)+1)
	for _, r := range d.runs {
		leases = append(leases, r.lease)
	}
	if d.leader != nil {
		leases = append(leases, *d.leader)
	}
	_ = d.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		for _, lease := range leases {
			// A lease lost to a successor answers STALE_FENCING_TOKEN; the
			// others are still released.
			_ = tx.ReleaseLease(ctx, lease)
		}
		return nil
	})
	d.leader, d.runs = nil, map[core.RunID]*activeRun{}
	d.health.Leader, d.health.Runs = false, nil
}

// lead renews the dispatcher lease, or takes it when it is free. Losing it
// drops every run this instance held: the next active dispatcher takes them
// over from the stored state.
func (d *Dispatcher) lead(ctx context.Context, res *TickResult) error {
	var lease *state.Lease
	lost := false
	err := d.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		lease, lost = nil, false
		if d.leader != nil {
			renewed, err := tx.RenewLease(ctx, *d.leader, d.cfg.LeaseTTL)
			if err == nil {
				lease = &renewed
				return nil
			}
			if !core.HasCode(err, core.CodeStaleFencingToken) && !core.HasCode(err, core.CodeExpired) {
				return err
			}
			lost = true
		}
		acquired, err := tx.AcquireLease(ctx, LeaseResource, d.cfg.Owner, d.cfg.LeaseTTL)
		switch {
		case core.HasCode(err, core.CodeLeaseHeld):
			return nil
		case err != nil:
			return err
		}
		lease = &acquired
		return nil
	})
	if err != nil {
		return err
	}
	if lost || lease == nil {
		for _, r := range d.runs {
			res.Lost = append(res.Lost, r.issue)
		}
		d.runs = map[core.RunID]*activeRun{}
	}
	d.leader = lease
	res.Leader = lease != nil
	return nil
}

// Health is a snapshot of the dispatcher for probes and the dashboard.
type Health struct {
	Owner string `json:"owner"`
	// Leader is true while this instance holds the dispatcher lease.
	Leader         bool      `json:"leader"`
	LeaseExpiresAt time.Time `json:"lease_expires_at,omitzero"`
	Runs           []RunInfo `json:"runs,omitempty"`
	LastTick       time.Time `json:"last_tick,omitzero"`
	// Held is the code that kept Ready issues waiting at the last tick.
	Held core.ErrorCode `json:"held,omitempty"`
	// LastError is the first failure of the last tick, if any.
	LastError string `json:"last_error,omitempty"`
}

// RunInfo is one run this instance holds the issue lease of.
type RunInfo struct {
	Issue          core.IssueRef     `json:"issue"`
	RunID          core.RunID        `json:"run_id"`
	FencingToken   core.FencingToken `json:"fencing_token"`
	LeaseExpiresAt time.Time         `json:"lease_expires_at"`
}

// Health returns the state of the dispatcher after its last tick.
func (d *Dispatcher) Health() Health {
	d.mu.Lock()
	defer d.mu.Unlock()
	h := d.health
	h.Runs = slices.Clone(h.Runs)
	return h
}

// record keeps the outcome of a tick for Health.
func (d *Dispatcher) record(res TickResult, err error) {
	h := Health{Owner: d.cfg.Owner, Leader: res.Leader, LastTick: d.cfg.Clock.Now(), Held: core.CodeOf(res.Held)}
	if d.leader != nil {
		h.LeaseExpiresAt = d.leader.ExpiresAt
	}
	for _, r := range d.runs {
		h.Runs = append(h.Runs, RunInfo{Issue: r.issue, RunID: r.run, FencingToken: r.lease.Token, LeaseExpiresAt: r.lease.ExpiresAt})
	}
	slices.SortFunc(h.Runs, func(a, b RunInfo) int { return cmp.Compare(a.RunID, b.RunID) })
	switch {
	case err != nil:
		h.LastError = err.Error()
	case len(res.Errors) > 0:
		h.LastError = res.Errors[0].Error()
	}
	d.health = h
}

// randomRunID names a run with 96 random bits.
func randomRunID() core.RunID {
	var raw [12]byte
	// crypto/rand.Read never returns an error on supported platforms.
	_, _ = rand.Read(raw[:])
	return core.RunID("run-" + hex.EncodeToString(raw[:]))
}
