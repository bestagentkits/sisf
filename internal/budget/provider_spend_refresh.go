package budget

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// CostSource reads what a provider reports was spent. It runs in the
// control zone, with a credential no agent holds.
type CostSource interface {
	// Spent returns what the provider reports was spent from since until
	// now, and how far its data reaches (zero when it does not say).
	Spent(ctx context.Context, since time.Time) (spent core.Micros, coveredUntil time.Time, err error)
}

// ProviderSpendEvery is how often the control zone reads the providers'
// figures. The providers refresh theirs every few hours, and it stays well
// inside ProviderSpendMaxAge, so one failed read does not hold runs.
const ProviderSpendEvery = 15 * time.Minute

// ProviderSpendRefresher reads the spend of the providers the budget
// settings name and stores it for the gate. A provider the settings name
// that has no source here, as when its credential is not configured, is
// not read: its figure grows stale and capped runs wait, which is the safe
// side of a configuration gap.
type ProviderSpendRefresher struct {
	Store   Store
	Sources map[string]CostSource
	Clock   core.Clock
}

// Refresh reads the figures when the stored ones are older than
// ProviderSpendEvery or cover another period, and stores what it read. A
// provider that cannot be read keeps the rest from being stored, so the
// stored figures always hold every named provider at one instant.
func (r ProviderSpendRefresher) Refresh(ctx context.Context) error {
	var (
		settings Settings
		version  int64
		due      bool
	)
	now := r.Clock.Now()
	err := r.Store.View(ctx, func(ctx context.Context, l Ledger) error {
		s, _, found, err := loadSettings(ctx, l)
		if err != nil || !found || len(s.ProviderCostSources) == 0 {
			return err
		}
		settings = s
		p, v, have, err := LoadProviderSpend(ctx, l)
		switch {
		case core.HasCode(err, core.CodeTampered):
			// A damaged figure is replaced by the next read.
			entry, err := l.GetKV(ctx, SettingsNamespace, ProviderSpendKey)
			if err != nil {
				return err
			}
			version, due = entry.Version, true
			return nil
		case err != nil:
			return err
		}
		version = v
		due = !have || !p.PeriodStart.Equal(s.Period.Start(now)) || p.FetchedAt.After(now) || now.Sub(p.FetchedAt) >= ProviderSpendEvery
		return nil
	})
	if err != nil || !due {
		return err
	}
	period := settings.Period.Start(now)
	next := ProviderSpend{PeriodStart: period, FetchedAt: now, Sources: map[string]SourceSpend{}}
	var failures []error
	for _, name := range settings.ProviderCostSources {
		source, ok := r.Sources[name]
		if !ok {
			failures = append(failures, core.NewError(core.CodeUnavailable, "no credential is configured to read the spend of "+name))
			continue
		}
		spent, covered, err := source.Spent(ctx, period)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		next.Sources[name] = SourceSpend{SpentMicros: spent, CoveredUntil: covered}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return core.NewError(core.CodeInternal, "the provider spend cannot be encoded").WithCause(err)
	}
	return r.Store.Update(ctx, func(ctx context.Context, l Ledger) error {
		_, err := l.PutKV(ctx, state.KVEntry{Namespace: SettingsNamespace, Key: ProviderSpendKey, Value: raw, Version: version})
		return err
	})
}
