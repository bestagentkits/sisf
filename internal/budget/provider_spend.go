package budget

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// ProviderSpendKey is where the control zone keeps what the providers
// report was spent, beside the budget settings. Only the control zone
// writes the namespace; the agent zone, whose dispatcher consults the gate,
// reads it. An agent therefore cannot lower the figure that bounds what its
// own reports may claim.
const ProviderSpendKey = "provider_spend"

// ProviderSpendMaxAge is how long after the control zone last read the
// providers' figures they still bound new runs. Past it the control zone is
// taken to have stopped reading them, and capped runs wait. It is the age
// of the read, not of the provider's data, which lags by hours: a bound on
// that lag would hold the factory for good.
const ProviderSpendMaxAge = 2 * time.Hour

// ProviderSpend is what the providers report the factory spent in a budget
// period, as the control zone last read it.
type ProviderSpend struct {
	// PeriodStart is the start of the budget period the figures cover.
	PeriodStart time.Time `json:"period_start"`
	// FetchedAt is when the control zone read the figures, by its clock.
	FetchedAt time.Time `json:"fetched_at"`
	// Sources holds each provider's figure, by provider name.
	Sources map[string]SourceSpend `json:"sources"`
}

// SourceSpend is one provider's report of what was spent since the start
// of the period.
type SourceSpend struct {
	SpentMicros core.Micros `json:"spent_micros"`
	// CoveredUntil is how far the provider's data reaches; zero when the
	// provider does not say. Spending after it is not in SpentMicros yet.
	CoveredUntil time.Time `json:"covered_until,omitzero"`
}

// LoadProviderSpend reads the stored figures; found is false when none were
// stored. An unreadable document is TAMPERED.
func LoadProviderSpend(ctx context.Context, kv state.KV) (p ProviderSpend, version int64, found bool, err error) {
	entry, err := kv.GetKV(ctx, SettingsNamespace, ProviderSpendKey)
	if core.HasCode(err, core.CodeNotFound) {
		return ProviderSpend{}, 0, false, nil
	}
	if err != nil {
		return ProviderSpend{}, 0, false, err
	}
	if err := json.Unmarshal(entry.Value, &p); err != nil {
		return ProviderSpend{}, 0, false, core.NewError(core.CodeTampered, "the stored provider spend cannot be read")
	}
	return p, entry.Version, true, nil
}

// providerFloor returns the least the current period counts as spent: the
// sum of the figures of the providers the settings name. It holds, with
// BUDGET_EXCEEDED, when a named provider's figure is missing, damaged or
// was read longer than ProviderSpendMaxAge ago, since what the agents under-report
// there would go unseen. Figures read for an earlier period do not count
// and do not hold: at the start of a period the provider has not reported
// it yet, which the fresh read of the earlier period shows.
func providerFloor(ctx context.Context, kv state.KV, s Settings, now time.Time) (core.Micros, error) {
	if len(s.ProviderCostSources) == 0 {
		return 0, nil
	}
	held := func(why string) error {
		return core.NewError(core.CodeBudgetExceeded, why+"; capped runs wait until the control zone reads the provider's spend again",
			core.NextAction{Action: "budget.get", Description: "see which provider's spend is missing"})
	}
	p, _, found, err := LoadProviderSpend(ctx, kv)
	switch {
	case core.HasCode(err, core.CodeTampered):
		// The control zone replaces a damaged figure on its next read.
		return 0, held("the stored provider spend cannot be read")
	case err != nil:
		return 0, err
	case !found:
		return 0, held("no provider spend has been read yet")
	case p.FetchedAt.After(now) || now.Sub(p.FetchedAt) > ProviderSpendMaxAge:
		return 0, held("the provider spend was last read at " + p.FetchedAt.UTC().Format(time.RFC3339))
	}
	period := s.Period.Start(now)
	var floor core.Micros
	for _, source := range s.ProviderCostSources {
		spend, ok := p.Sources[source]
		if !ok {
			return 0, held("the spend of " + source + " was not read")
		}
		if p.PeriodStart.Equal(period) {
			floor = addMicros(floor, spend.SpentMicros)
		}
	}
	return floor, nil
}

// usedWith is what a period holds and has spent when the providers report
// at least floor spent: the agents' reports count only where they reach
// beyond it.
func usedWith(t core.BudgetTotals, floor core.Micros) core.Micros {
	return addMicros(t.Reserved, max(addMicros(t.SpentVerified, t.SpentUnverified), floor))
}
