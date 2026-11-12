package budget

import (
	"context"
	"errors"
	"time"

	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Status is what budget.get shows.
type Status struct {
	// Confirmed is false until the Owner confirmed the budget at install.
	Confirmed bool            `json:"confirmed"`
	Mode      core.BudgetMode `json:"mode"`
	Settings  Settings        `json:"settings"`
	// PeriodStart is when the current period began; zero without a period.
	PeriodStart time.Time `json:"period_start,omitzero"`
	// ReservedMicros is what held reservations set aside.
	ReservedMicros core.Micros `json:"reserved_micros"`
	// SpentVerifiedMicros was confirmed by the provider.
	SpentVerifiedMicros core.Micros `json:"spent_verified_micros"`
	// SpentUnverifiedMicros is usage the provider could not confirm; it is
	// shown apart and counts against the cap.
	SpentUnverifiedMicros core.Micros `json:"spent_unverified_micros"`
	// AvailableMicros is what a normal run may still reserve; capped only.
	AvailableMicros *core.Micros `json:"available_micros,omitempty"`
	// Warnings are plain-language notices about the level of guarantee.
	Warnings []string `json:"warnings"`
}

// ReadStatus reports the budget as the gate sees it. An unconfirmed budget is
// reported, not refused, so the Owner can see what is missing.
func ReadStatus(ctx context.Context, src dispatcher.ConfirmationSource, ledger Ledger, now time.Time) (Status, error) {
	st := Status{Mode: core.BudgetUnlimited, Warnings: []string{}}
	settings, err := effective(ctx, src, ledger, now)
	var typed *core.Error
	switch {
	case core.HasCode(err, core.CodeBudgetUnconfirmed) && errors.As(err, &typed):
		st.Warnings = append(st.Warnings, "budget not confirmed, no work is claimed: "+typed.Message)
		return st, nil
	case err != nil:
		return Status{}, err
	}
	st.Confirmed, st.Mode, st.Settings = true, settings.Mode, settings
	st.PeriodStart = settings.Period.Start(now)
	totals, err := ledger.BudgetTotals(ctx, st.PeriodStart)
	if err != nil {
		return Status{}, err
	}
	st.ReservedMicros, st.SpentVerifiedMicros, st.SpentUnverifiedMicros = totals.Reserved, totals.SpentVerified, totals.SpentUnverified
	if settings.Mode == core.BudgetCapped {
		available := settings.CapMicros - settings.RollbackReserveMicros - totals.Reserved - totals.SpentVerified - totals.SpentUnverified
		st.AvailableMicros = &available
	}
	if totals.SpentUnverified > 0 {
		st.Warnings = append(st.Warnings, "some usage is unverified: the provider could not confirm the amount")
	}
	if charged := totals.Unpriced - totals.UnpricedUncharged; charged > 0 {
		st.Warnings = append(st.Warnings, "some usage had no cost reported by its provider: it is charged at the amount reserved for its run, as unverified spending")
	}
	if totals.UnpricedUncharged > 0 {
		st.Warnings = append(st.Warnings, "some usage had no cost reported by its provider and its run reserved nothing: its cost is not counted in the spending shown")
	}
	if !settings.ProviderHardCap {
		st.Warnings = append(st.Warnings, "the provider has no hard cap: the factory limits what it starts, but the provider may still charge beyond it")
	}
	return st, nil
}
