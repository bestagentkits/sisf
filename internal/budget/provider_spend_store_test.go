package budget_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// fakeSource is a provider's report of spending, as a test sets it.
type fakeSource struct {
	mu    sync.Mutex
	spent core.Micros
	err   error
	since []time.Time
}

func (f *fakeSource) Spent(_ context.Context, since time.Time) (core.Micros, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.since = append(f.since, since)
	return f.spent, since.Add(time.Hour), f.err
}

func (f *fakeSource) reads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.since)
}

// providerStore is a capped budget checked against the named providers,
// as the control zone stores it, and the stores of the control and agent
// zones; on PostgreSQL the agent connects with its own login role.
func providerStore(t *testing.T, db *sqlstoretest.Database, sources ...string) (control, agent *sqlstore.Store, agentDSN string) {
	t.Helper()
	ctx := context.Background()
	control = db.Open(t, core.ZoneControl)
	agent = db.Open(t, core.ZoneAgent)
	if db.Dialect == sqlstore.DialectPostgres {
		agentDSN = db.ZoneLogins(t).DSN[core.ZoneAgent]
		agent = db.OpenWith(t, agentDSN, core.ZoneAgent)
	}
	s := capped(10*million, million)
	s.Period, s.ProviderCostSources = budget.PeriodDay, sources
	for key, value := range map[string]any{
		dispatcher.ConfirmationKey: dispatcher.BudgetConfirmation{Mode: core.BudgetCapped, AlertOff: true, ConfirmedAt: db.Clock.Now().Add(-time.Hour)},
		budget.SettingsKey:         s,
	} {
		raw, _ := json.Marshal(value)
		if err := control.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			_, err := tx.PutKV(ctx, state.KVEntry{Namespace: budget.SettingsNamespace, Key: key, Value: raw})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	return control, agent, agentDSN
}

func reserveIn(t *testing.T, db *sqlstoretest.Database, store *sqlstore.Store, n int, estimate core.Micros) error {
	t.Helper()
	gate := budget.NewGate(budget.Config{Clock: db.Clock})
	req := request(n, estimate)
	req.FactoryID = sqlstoretest.Factory
	return store.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		_, err := gate.Reserve(ctx, tx, req)
		return err
	})
}

// The control zone reads the provider's spend and stores it where the
// agent zone's gate reads it; the agent zone cannot write it, through the
// store or, on PostgreSQL, over plain SQL as its own role, so it cannot
// lower the figure that bounds what its reports may claim.
func TestOnlyTheControlZoneWritesTheProviderSpend(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		control, agent, agentDSN := providerStore(t, db, budget.ProviderAnthropic)
		source := &fakeSource{spent: 9 * million}
		refresher := budget.ProviderSpendRefresher{Store: budget.StateStore(control),
			Sources: map[string]budget.CostSource{budget.ProviderAnthropic: source}, Clock: db.Clock}
		if err := reserveIn(t, db, agent, 1, million); !core.HasCode(err, core.CodeBudgetExceeded) {
			t.Fatalf("a run admitted before the provider's spend was read: %v", err)
		}
		if err := refresher.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		day := budget.PeriodDay.Start(db.Clock.Now())
		if len(source.since) != 1 || !source.since[0].Equal(day) {
			t.Fatalf("the provider was asked for %v, want the spend since %v", source.since, day)
		}
		if err := reserveIn(t, db, agent, 2, million); !core.HasCode(err, core.CodeBudgetExceeded) {
			t.Fatalf("a run admitted past the provider's figure: %v", err)
		}

		forged, _ := json.Marshal(budget.ProviderSpend{PeriodStart: day, FetchedAt: db.Clock.Now(),
			Sources: map[string]budget.SourceSpend{budget.ProviderAnthropic: {}}})
		err := agent.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			entry, err := tx.GetKV(ctx, budget.SettingsNamespace, budget.ProviderSpendKey)
			if err != nil {
				return err
			}
			entry.Value = forged
			_, err = tx.PutKV(ctx, entry)
			return err
		})
		if !core.HasCode(err, core.CodePermissionDenied) {
			t.Fatalf("the agent zone rewrote the provider spend: %v", err)
		}
		if db.Dialect != sqlstore.DialectPostgres {
			return
		}
		raw, err := sql.Open("pgx", agentDSN)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = raw.Close() })
		for _, statement := range []string{
			`UPDATE kv SET entry_value = '\x7b7d' WHERE namespace = 'install' AND entry_key = 'provider_spend'`,
			`DELETE FROM kv WHERE namespace = 'install' AND entry_key = 'provider_spend'`,
			`INSERT INTO kv (factory_id, namespace, entry_key, entry_value, version, updated_at) VALUES ('other', 'install', 'provider_spend', '\x7b7d', 1, 0)`,
		} {
			res, err := raw.ExecContext(ctx, statement)
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "42501" {
				continue
			}
			if err == nil {
				// A row policy hides the rows instead of refusing.
				if n, _ := res.RowsAffected(); n == 0 {
					continue
				}
			}
			t.Errorf("the agent role must not change the provider spend with %q: %v", statement, err)
		}
		if err := reserveIn(t, db, agent, 3, million); !core.HasCode(err, core.CodeBudgetExceeded) {
			t.Fatalf("the provider's figure was lowered: %v", err)
		}
	})
}

// The figures are read again only every ProviderSpendEvery, or at once in a
// new period. A provider that cannot be read, or has no credential, stores
// nothing, so its figure grows stale and holds runs: a configuration gap
// fails closed.
func TestTheProviderSpendIsReadOnScheduleAndAFailureStoresNothing(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		control, agent, _ := providerStore(t, db, budget.ProviderAnthropic, budget.ProviderOpenAI)
		anthropic, openai := &fakeSource{spent: million}, &fakeSource{spent: million}
		refresher := budget.ProviderSpendRefresher{Store: budget.StateStore(control), Clock: db.Clock,
			Sources: map[string]budget.CostSource{budget.ProviderAnthropic: anthropic, budget.ProviderOpenAI: openai}}
		if err := refresher.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		db.Clock.Advance(budget.ProviderSpendEvery - time.Second)
		if err := refresher.Refresh(ctx); err != nil || anthropic.reads() != 1 {
			t.Fatalf("read again too soon: %d reads, %v", anthropic.reads(), err)
		}
		db.Clock.Advance(time.Second)
		if err := refresher.Refresh(ctx); err != nil || anthropic.reads() != 2 {
			t.Fatalf("not read again on schedule: %d reads, %v", anthropic.reads(), err)
		}
		if err := reserveIn(t, db, agent, 1, million); err != nil {
			t.Fatalf("a run within the providers' figures: %v", err)
		}

		// One provider fails: nothing is stored, and once the last read is
		// too old, runs wait while a rollback still runs.
		openai.err = core.NewError(core.CodeUnavailable, "the provider is down")
		for range 9 {
			db.Clock.Advance(budget.ProviderSpendEvery)
			if err := refresher.Refresh(ctx); !core.HasCode(err, core.CodeUnavailable) {
				t.Fatalf("a failed read: %v", err)
			}
		}
		if err := reserveIn(t, db, agent, 2, million); !core.HasCode(err, core.CodeBudgetExceeded) {
			t.Fatalf("a run admitted on a stale provider figure: %v", err)
		}
		gate := budget.NewGate(budget.Config{Clock: db.Clock})
		rollback := request(3, million)
		rollback.FactoryID = sqlstoretest.Factory
		if err := agent.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			_, err := gate.ReserveRollback(ctx, tx, rollback)
			return err
		}); err != nil {
			t.Fatalf("a rollback held by a stale provider figure: %v", err)
		}

		// A provider without a credential is not read and stores nothing.
		delete(refresher.Sources, budget.ProviderOpenAI)
		if err := refresher.Refresh(ctx); !core.HasCode(err, core.CodeUnavailable) {
			t.Fatalf("a provider without a credential: %v", err)
		}
		if err := reserveIn(t, db, agent, 4, million); !core.HasCode(err, core.CodeBudgetExceeded) {
			t.Fatalf("a run admitted without one provider's figure: %v", err)
		}
	})
}

// A damaged figure is replaced by the next read instead of blocking it.
func TestADamagedProviderSpendIsReadAgain(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		control, agent, _ := providerStore(t, db, budget.ProviderAnthropic)
		if err := control.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			_, err := tx.PutKV(ctx, state.KVEntry{Namespace: budget.SettingsNamespace, Key: budget.ProviderSpendKey, Value: []byte("{")})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := reserveIn(t, db, agent, 1, million); !core.HasCode(err, core.CodeTampered) {
			t.Fatalf("a run admitted on a damaged provider figure: %v", err)
		}
		refresher := budget.ProviderSpendRefresher{Store: budget.StateStore(control), Clock: db.Clock,
			Sources: map[string]budget.CostSource{budget.ProviderAnthropic: &fakeSource{}}}
		if err := refresher.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		if err := reserveIn(t, db, agent, 2, million); err != nil {
			t.Fatalf("a run after the damaged figure was read again: %v", err)
		}
	})
}
