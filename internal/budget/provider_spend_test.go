package budget_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
)

// day is the start of the budget day that holds start.
var day = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)

// checkedAgainst is a daily cap of 10 that the provider's own report of
// spending bounds.
func checkedAgainst(sources ...string) budget.Settings {
	s := capped(10*million, million)
	s.Period, s.ProviderCostSources = budget.PeriodDay, sources
	return s
}

// providerReports stores what the control zone read from the providers.
func (e *env) providerReports(period, fetched time.Time, spent map[string]core.Micros) {
	e.t.Helper()
	p := budget.ProviderSpend{PeriodStart: period, FetchedAt: fetched, Sources: map[string]budget.SourceSpend{}}
	for name, micros := range spent {
		p.Sources[name] = budget.SourceSpend{SpentMicros: micros, CoveredUntil: fetched.Add(-4 * time.Hour)}
	}
	e.put(budget.ProviderSpendKey, p)
}

// agentReports books cost as reported by an agent today.
func (e *env) agentReports(n int, cost core.Micros) {
	e.t.Helper()
	u := usage(n, cost, false)
	u.At = start
	if err := e.ledger.AppendUsage(context.Background(), u); err != nil {
		e.t.Fatal(err)
	}
}

// An agent zone that under-reports what its runs cost is caught once the
// provider reports more: the provider's figure is what the period counts as
// spent, so the cap stops runs the agents' own reports would still admit.
func TestTheProvidersReportBoundsWhatTheAgentsReport(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.confirmCapped()
	e.configure(checkedAgainst(budget.ProviderAnthropic))
	e.agentReports(1, million)
	e.providerReports(day, start.Add(-time.Minute), map[string]core.Micros{budget.ProviderAnthropic: 8 * million})

	// 8 spent and a reserve of 1 leave 1 to reserve, not the 8 the agents'
	// reports would leave.
	_, err := e.reserve(2, 2*million)
	wantCode(t, err, core.CodeBudgetExceeded)
	e.mustReserve(3, million)

	st := e.readStatus()
	if st.ProviderSpentMicros == nil || *st.ProviderSpentMicros != 8*million {
		t.Fatalf("the provider spend shown: %v, want 8", st.ProviderSpentMicros)
	}
	if st.SpentUnverifiedMicros != million {
		t.Errorf("the agents' reports shown as %d, want them kept apart from the provider's figure", st.SpentUnverifiedMicros)
	}
	if st.AvailableMicros == nil || *st.AvailableMicros != 0 {
		t.Errorf("available: %v, want 0 after the reservation", st.AvailableMicros)
	}
	if !warned(st, "under-report") {
		t.Errorf("no warning that the provider reports more than the agents: %v", st.Warnings)
	}
}

// The agents' reports still count where they reach beyond the provider's
// figure, which lags by hours.
func TestTheAgentsReportsCountBeyondALaggingProviderFigure(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.confirmCapped()
	e.configure(checkedAgainst(budget.ProviderAnthropic, budget.ProviderOpenAI))
	e.agentReports(1, 7*million)
	e.providerReports(day, start.Add(-time.Minute), map[string]core.Micros{budget.ProviderAnthropic: million, budget.ProviderOpenAI: million})
	_, err := e.reserve(2, 3*million)
	wantCode(t, err, core.CodeBudgetExceeded)
	e.mustReserve(3, 2*million)
	if st := e.readStatus(); warned(st, "under-report") {
		t.Errorf("a provider figure below the agents' reports was warned about: %v", st.Warnings)
	}
}

// A named provider whose figure is missing, partial, stale or dated in the
// future holds capped runs, since what the agents under-report there would
// go unseen; a rollback still runs, held to the agents' reports and the cap.
func TestAMissingOrStaleProviderFigureHoldsRunsButNotARollback(t *testing.T) {
	t.Parallel()
	cases := map[string]func(e *env){
		"never read": func(*env) {},
		"read too long ago": func(e *env) {
			e.providerReports(day, start.Add(-budget.ProviderSpendMaxAge-time.Second), map[string]core.Micros{budget.ProviderAnthropic: 0})
		},
		"read in the future": func(e *env) {
			e.providerReports(day, start.Add(time.Minute), map[string]core.Micros{budget.ProviderAnthropic: 0})
		},
		"another provider read": func(e *env) {
			e.providerReports(day, start.Add(-time.Minute), map[string]core.Micros{budget.ProviderOpenAI: 0})
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.confirmCapped()
			e.configure(checkedAgainst(budget.ProviderAnthropic))
			setup(e)
			_, err := e.reserve(1, million)
			wantCode(t, err, core.CodeBudgetExceeded)
			if _, err := e.gate.ReserveRollback(context.Background(), e.ledger, request(2, million)); err != nil {
				t.Fatalf("a rollback was held by the provider figure: %v", err)
			}
			if st := e.readStatus(); !warned(st, "held") || st.ProviderSpentMicros != nil {
				t.Errorf("the status of a held budget: %+v", st)
			}
		})
	}
}

// At the start of a period the provider has reported nothing of it yet: a
// fresh read of the period before neither counts nor holds runs.
func TestAFigureOfTheLastPeriodNeitherCountsNorHolds(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.confirmCapped()
	e.configure(checkedAgainst(budget.ProviderAnthropic))
	e.providerReports(day.AddDate(0, 0, -1), start.Add(-time.Minute), map[string]core.Micros{budget.ProviderAnthropic: 9 * million})
	e.mustReserve(1, 8*million)
}

// Without a named provider the gate reads no provider figure at all.
func TestNoProviderNamedReadsNoFigure(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.confirmCapped()
	s := checkedAgainst()
	e.configure(s)
	e.providerReports(day, start.Add(-time.Minute), map[string]core.Micros{budget.ProviderAnthropic: 9 * million})
	e.mustReserve(1, 8*million)
}

func TestProviderCostSourcesNeedACapAndAPeriod(t *testing.T) {
	t.Parallel()
	bad := map[string]budget.Settings{
		"unlimited":   {Mode: core.BudgetUnlimited, ProviderCostSources: []string{budget.ProviderAnthropic}},
		"no period":   {Mode: core.BudgetCapped, CapMicros: million, ProviderCostSources: []string{budget.ProviderAnthropic}},
		"unknown":     {Mode: core.BudgetCapped, CapMicros: million, Period: budget.PeriodDay, ProviderCostSources: []string{"acme"}},
		"named twice": {Mode: core.BudgetCapped, CapMicros: million, Period: budget.PeriodDay, ProviderCostSources: []string{"openai", "openai"}},
		"empty name":  {Mode: core.BudgetCapped, CapMicros: million, Period: budget.PeriodDay, ProviderCostSources: []string{""}},
	}
	for name, s := range bad {
		if err := s.Validate(); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: %v, want INVALID_ARGUMENT", name, err)
		}
	}
	if err := checkedAgainst(budget.ProviderAnthropic, budget.ProviderOpenAI).Validate(); err != nil {
		t.Errorf("a daily cap checked against both providers: %v", err)
	}
}

// confirmCapped confirms a capped budget at install, an hour before start.
func (e *env) confirmCapped() {
	e.t.Helper()
	e.confirm(dispatcher.BudgetConfirmation{Mode: core.BudgetCapped, AlertOff: true, ConfirmedAt: start.Add(-time.Hour)})
}
