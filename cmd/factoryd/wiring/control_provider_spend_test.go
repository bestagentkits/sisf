package wiring_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// spendOf is a provider that reports a fixed spend.
type spendOf core.Micros

func (s spendOf) Spent(_ context.Context, since time.Time) (core.Micros, time.Time, error) {
	return core.Micros(s), since, nil
}

// A control pass reads the spend of the providers the budget settings name
// and stores it where the agent zone's gate reads it.
func TestAControlPassReadsTheProvidersSpend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{ProviderCost: map[string]budget.CostSource{budget.ProviderAnthropic: spendOf(3_000_000)}})
	settings, _ := json.Marshal(budget.Settings{Mode: core.BudgetCapped, CapMicros: 10_000_000, Period: budget.PeriodDay,
		ProviderHardCap: true, ProviderCostSources: []string{budget.ProviderAnthropic}})
	if err := f.ControlStore.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		_, err := tx.PutKV(ctx, state.KVEntry{Namespace: budget.SettingsNamespace, Key: budget.SettingsKey, Value: settings})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.Pass()
	var spend budget.ProviderSpend
	if err := f.ControlStore.View(ctx, func(ctx context.Context, tx state.Tx) (err error) {
		spend, _, _, err = budget.LoadProviderSpend(ctx, tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := spend.Sources[budget.ProviderAnthropic].SpentMicros; got != 3_000_000 || !spend.FetchedAt.Equal(f.Clock.Now()) {
		t.Fatalf("the stored provider spend: %+v, want 3 read now", spend)
	}
}
