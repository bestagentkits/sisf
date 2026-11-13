package budget_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

const (
	factory core.FactoryID = "acme-app"
	million core.Micros    = 1_000_000
)

// Monday 2026-11-09 morning, so a week and a month boundary are near.
var start = time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)

// memLedger is an in-memory budget ledger with documents, as one store
// transaction offers them. A test can make appending usage fail.
type memLedger struct {
	mu           sync.Mutex
	kv           map[string]state.KVEntry
	reservations map[string]core.Reservation
	usage        []core.Usage
	failAppend   error
}

func newLedger() *memLedger {
	return &memLedger{kv: map[string]state.KVEntry{}, reservations: map[string]core.Reservation{}}
}

func (l *memLedger) PutReservation(_ context.Context, r core.Reservation) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reservations[r.ID] = r
	return nil
}

func (l *memLedger) GetReservation(_ context.Context, id string) (core.Reservation, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r, ok := l.reservations[id]; ok {
		return r, nil
	}
	return core.Reservation{}, core.NewError(core.CodeNotFound, "no such reservation")
}

func (l *memLedger) AppendUsage(_ context.Context, u core.Usage) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failAppend != nil {
		return l.failAppend
	}
	l.usage = append(l.usage, u)
	return nil
}

func (l *memLedger) RunUsage(_ context.Context, run core.RunID) (core.RunUsage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var sum core.RunUsage
	for _, u := range l.usage {
		if u.RunID == run {
			sum.Cost += u.Cost
			sum.CostUnreported = sum.CostUnreported || u.CostUnreported()
			sum.Entries++
		}
	}
	return sum, nil
}

func (l *memLedger) BudgetTotals(_ context.Context, since time.Time) (core.BudgetTotals, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var t core.BudgetTotals
	for _, r := range l.reservations {
		if r.Status == core.ReservationHeld {
			t.Reserved += r.Amount
		}
	}
	for _, u := range l.usage {
		switch {
		case u.At.Before(since):
			continue
		case u.Verified:
			t.SpentVerified += u.Cost
		default:
			t.SpentUnverified += u.Cost
		}
		if u.CostUnreported() {
			t.Unpriced++
		}
	}
	return t, nil
}

func (l *memLedger) GetKV(_ context.Context, ns, key string) (state.KVEntry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.kv[ns+"/"+key]; ok {
		return e, nil
	}
	return state.KVEntry{}, core.NewError(core.CodeNotFound, "no such entry")
}

func (l *memLedger) PutKV(_ context.Context, e state.KVEntry) (state.KVEntry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if cur := l.kv[e.Namespace+"/"+e.Key]; cur.Version != e.Version {
		return state.KVEntry{}, core.NewError(core.CodeConflict, "version")
	}
	e.Version++
	l.kv[e.Namespace+"/"+e.Key] = e
	return e, nil
}

func (l *memLedger) ListKV(context.Context, string, string, int) ([]state.KVEntry, error) {
	return nil, errors.New("not modelled")
}

func (l *memLedger) DeleteKV(context.Context, string, string, int64) error {
	return errors.New("not modelled")
}

// View and Update make the ledger a budget.Store.
func (l *memLedger) View(ctx context.Context, fn func(context.Context, budget.Ledger) error) error {
	return fn(ctx, l)
}

func (l *memLedger) Update(ctx context.Context, fn func(context.Context, budget.Ledger) error) error {
	return fn(ctx, l)
}

// notifier records the notices it is given.
type notifier struct {
	mu    sync.Mutex
	items []budget.Notice
}

func (n *notifier) Notify(_ context.Context, notice budget.Notice) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.items = append(n.items, notice)
	return errors.New("the channel is down") // a failing channel must not change any decision
}

func (n *notifier) kinds() []budget.NoticeKind {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []budget.NoticeKind
	for _, i := range n.items {
		out = append(out, i.Kind)
	}
	return out
}

// env is a gate over an in-memory ledger and a clock only the test moves.
type env struct {
	t      *testing.T
	clock  *intaketest.Clock
	ledger *memLedger
	notes  *notifier
	gate   *budget.Gate
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, clock: intaketest.NewClock(start), ledger: newLedger(), notes: &notifier{}}
	e.gate = budget.NewGate(budget.Config{Clock: e.clock, Notifier: e.notes})
	return e
}

// confirm stores the install-time confirmation where the installer writes it.
func (e *env) confirm(c dispatcher.BudgetConfirmation) {
	e.t.Helper()
	e.put(dispatcher.ConfirmationKey, c)
}

// configure stores the Owner's settings, as budget.set does.
func (e *env) configure(s budget.Settings) {
	e.t.Helper()
	e.put(budget.SettingsKey, s)
}

func (e *env) confirmUnlimited(alert core.Micros) {
	e.t.Helper()
	e.confirm(dispatcher.BudgetConfirmation{Mode: core.BudgetUnlimited, AlertThresholdMicros: alert, AlertOff: alert == 0,
		ConfirmedAt: start.Add(-time.Hour)})
}
