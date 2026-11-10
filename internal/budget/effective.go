package budget

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

func unconfirmed(msg string) error { return core.NewError(core.CodeBudgetUnconfirmed, msg) }

// confirmed checks the install-time confirmation. Without a complete one no
// run is claimed, whatever settings exist.
func confirmed(ctx context.Context, src dispatcher.ConfirmationSource, ledger core.BudgetLedger, now time.Time) (dispatcher.BudgetConfirmation, error) {
	c, err := src.BudgetConfirmation(ctx, ledger)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return c, core.NewError(core.CodeBudgetUnconfirmed,
			"the Owner has not confirmed the budget mode yet, so no work is claimed").WithCause(err)
	case err != nil:
		return c, err
	case !c.Mode.Valid():
		return c, unconfirmed("the confirmation does not state a known budget mode")
	case c.ConfirmedAt.IsZero() || c.ConfirmedAt.After(now):
		return c, unconfirmed("the confirmation does not carry a past instant")
	case c.AlertThresholdMicros < 0:
		return c, unconfirmed("the alert threshold must not be negative")
	case c.Mode == core.BudgetUnlimited && c.AlertThresholdMicros == 0 && !c.AlertOff:
		return c, unconfirmed("an unlimited budget needs an alert threshold or an explicit choice of no alert")
	}
	return c, nil
}

// effective returns the settings in force: the Owner's settings when they
// changed them, otherwise what the install confirmed. It refuses with
// BUDGET_UNCONFIRMED until the install confirmation is complete.
func effective(ctx context.Context, src dispatcher.ConfirmationSource, ledger core.BudgetLedger, now time.Time) (Settings, error) {
	c, err := confirmed(ctx, src, ledger, now)
	if err != nil {
		return Settings{}, err
	}
	kv, ok := ledger.(state.KV)
	if !ok {
		return Settings{}, core.NewError(core.CodeUnavailable, "the claim transaction cannot read the budget settings")
	}
	stored, _, found, err := loadSettings(ctx, kv)
	switch {
	case err != nil:
		return Settings{}, err
	case found:
		return stored, nil
	case c.Mode == core.BudgetCapped:
		return Settings{}, unconfirmed("a capped budget needs its cap, which the Owner has not set")
	}
	return Settings{Mode: c.Mode, AlertThresholdMicros: c.AlertThresholdMicros}, nil
}
