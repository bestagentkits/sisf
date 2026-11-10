package budget_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
)

func (e *env) readStatus() budget.Status {
	e.t.Helper()
	st, err := budget.ReadStatus(context.Background(), dispatcher.KVConfirmation{}, e.ledger, e.clock.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

func warned(st budget.Status, fragment string) bool {
	for _, w := range st.Warnings {
		if strings.Contains(w, fragment) {
			return true
		}
	}
	return false
}

func TestUnverifiedUsageAndAMissingHardCapAreShown(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.confirmUnlimited(0)
	s := capped(10*million, 0)
	s.ProviderHardCap = false
	e.configure(s)
	r := e.mustReserve(1, 4*million)
	if r.HardCapEnforced {
		t.Error("a budget with no provider hard cap claims one")
	}
	if err := e.gate.Settle(context.Background(), e.ledger, r.ID, usage(1, 3*million, false)); err != nil {
		t.Fatal(err)
	}
	st := e.readStatus()
	if st.SpentUnverifiedMicros != 3*million || st.SpentVerifiedMicros != 0 || !warned(st, "unverified") || !warned(st, "no hard cap") {
		t.Errorf("status = %+v", st)
	}
	if st.AvailableMicros == nil || *st.AvailableMicros != 7*million {
		t.Errorf("unverified usage did not count against the cap: %+v", st.AvailableMicros)
	}
}

func TestStatusOfAnUnconfirmedBudgetSaysSoInsteadOfFailing(t *testing.T) {
	t.Parallel()
	st := newEnv(t).readStatus()
	if st.Confirmed || !warned(st, "not confirmed") {
		t.Errorf("status = %+v", st)
	}
}

func TestPeriodStartsAtTheBoundaryOfDayWeekAndMonth(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 11, 15, 30, 0, 0, time.UTC) // a Wednesday
	want := map[budget.Period]time.Time{
		budget.PeriodDay:   time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC),
		budget.PeriodWeek:  time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC),
		budget.PeriodMonth: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		budget.PeriodNone:  {},
	}
	for p, w := range want {
		if got := p.Start(now); !got.Equal(w) {
			t.Errorf("%q starts at %v, want %v", p, got, w)
		}
	}
	if got := budget.PeriodWeek.Start(time.Date(2026, 11, 15, 23, 0, 0, 0, time.UTC)); got.Day() != 9 {
		t.Errorf("a Sunday belongs to the week that began on Monday the 9th, got %v", got)
	}
}

func TestSettingsRefuseWhatCannotBeEnforced(t *testing.T) {
	t.Parallel()
	bad := map[string]budget.Settings{
		"no mode":                   {},
		"capped without a cap":      {Mode: core.BudgetCapped},
		"reserve not below the cap": {Mode: core.BudgetCapped, CapMicros: million, RollbackReserveMicros: million},
		"cap while unlimited":       {Mode: core.BudgetUnlimited, CapMicros: million},
		"negative alert":            {Mode: core.BudgetUnlimited, AlertThresholdMicros: -1},
		"unknown period":            {Mode: core.BudgetUnlimited, Period: "year"},
	}
	for name, s := range bad {
		if err := s.Validate(); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: error = %v, want INVALID_ARGUMENT", name, err)
		}
	}
}
