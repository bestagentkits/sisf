package budget

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Where the Owner's budget settings live in the state store. The control
// zone owns this namespace and the agent zone, whose dispatcher consults the
// gate, may read it; the installer's confirmation lives beside it.
const (
	SettingsNamespace = "install"
	SettingsKey       = "budget_settings"
)

// Period says when the spent amount of a capped budget starts again from zero.
type Period string

const (
	// PeriodNone never resets: the cap covers everything ever spent.
	PeriodNone  Period = ""
	PeriodDay   Period = "day"
	PeriodWeek  Period = "week"
	PeriodMonth Period = "month"
)

// Valid reports whether p is a known period.
func (p Period) Valid() bool {
	return p == PeriodNone || p == PeriodDay || p == PeriodWeek || p == PeriodMonth
}

// Start returns the first instant of the period that contains now, in UTC.
// A week starts on Monday. Without a period it is the zero time, so
// everything counts.
func (p Period) Start(now time.Time) time.Time {
	now = now.UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	switch p {
	case PeriodDay:
		return day
	case PeriodWeek:
		return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
	case PeriodMonth:
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Time{}
}

// Settings is what the Owner decided about spending.
type Settings struct {
	Mode core.BudgetMode `json:"mode"`
	// CapMicros is the most a period may spend; capped mode only.
	CapMicros core.Micros `json:"cap_micros,omitempty"`
	Period    Period      `json:"period,omitempty"`
	// AlertThresholdMicros notifies the Owner once spending in the period
	// passes it. It never stops anything; zero means no alert.
	AlertThresholdMicros core.Micros `json:"alert_threshold_micros,omitempty"`
	// RollbackReserveMicros is kept out of reach of normal runs, so a
	// rollback can still run when the cap is used up; capped mode only.
	RollbackReserveMicros core.Micros `json:"rollback_reserve_micros,omitempty"`
	// ProviderHardCap states that the provider account enforces a hard limit
	// of its own. When false the budget is flagged as having no hard cap.
	ProviderHardCap bool `json:"provider_hard_cap,omitempty"`
}

// Validate rejects settings that are ambiguous or cannot be enforced.
func (s Settings) Validate() error {
	bad := func(msg string) error { return core.NewError(core.CodeInvalidArgument, msg) }
	switch {
	case !s.Mode.Valid():
		return bad("the budget mode must be unlimited or capped")
	case !s.Period.Valid():
		return bad("the budget period must be none, day, week or month")
	case s.CapMicros < 0 || s.AlertThresholdMicros < 0 || s.RollbackReserveMicros < 0:
		return bad("budget amounts must not be negative")
	case s.Mode == core.BudgetUnlimited && (s.CapMicros != 0 || s.RollbackReserveMicros != 0):
		return bad("an unlimited budget has no cap or rollback reserve; choose capped to set them")
	case s.Mode == core.BudgetCapped && s.CapMicros == 0:
		return bad("a capped budget needs a cap above zero")
	case s.Mode == core.BudgetCapped && s.RollbackReserveMicros >= s.CapMicros:
		return bad("the rollback reserve must be smaller than the cap")
	}
	return nil
}

// Ledger is what the gate and the actions work against: the budget ledger
// and the documents of one transaction. A state-store transaction is one.
type Ledger interface {
	core.BudgetLedger
	state.KV
}

// Store opens transactions that carry a Ledger.
type Store interface {
	View(ctx context.Context, fn func(ctx context.Context, l Ledger) error) error
	Update(ctx context.Context, fn func(ctx context.Context, l Ledger) error) error
}

// StateStore adapts a state store to Store.
func StateStore(s state.Store) Store { return stateStore{s} }

type stateStore struct{ s state.Store }

func (a stateStore) View(ctx context.Context, fn func(context.Context, Ledger) error) error {
	return a.s.View(ctx, func(ctx context.Context, tx state.Tx) error { return fn(ctx, tx) })
}

func (a stateStore) Update(ctx context.Context, fn func(context.Context, Ledger) error) error {
	return a.s.Update(ctx, func(ctx context.Context, tx state.Tx) error { return fn(ctx, tx) })
}

// loadSettings reads the stored settings; found is false when the Owner has
// not changed them since install. Unreadable or invalid settings are TAMPERED,
// so a damaged document never loosens a cap.
func loadSettings(ctx context.Context, kv state.KV) (s Settings, version int64, found bool, err error) {
	entry, err := kv.GetKV(ctx, SettingsNamespace, SettingsKey)
	if core.HasCode(err, core.CodeNotFound) {
		return Settings{}, 0, false, nil
	}
	if err != nil {
		return Settings{}, 0, false, err
	}
	if err := json.Unmarshal(entry.Value, &s); err != nil || s.Validate() != nil {
		return Settings{}, 0, false, core.NewError(core.CodeTampered, "the stored budget settings cannot be read")
	}
	return s, entry.Version, true, nil
}

// saveSettings writes the settings over the version that was read.
func saveSettings(ctx context.Context, kv state.KV, s Settings, version int64) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return core.NewError(core.CodeInternal, "the budget settings cannot be encoded").WithCause(err)
	}
	_, err = kv.PutKV(ctx, state.KVEntry{Namespace: SettingsNamespace, Key: SettingsKey, Value: raw, Version: version})
	return err
}
