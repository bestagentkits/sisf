package main

import "github.com/bestagentkits/sisf/internal/install"

// budget turns the flags into the Owner's answer; given says whether the
// Owner answered on the command line at all.
func (f installFlags) budget() (choice install.BudgetChoice, given bool, err error) {
	given = f.confirmUnlimited || f.noAlert || f.alertThreshold != "" || f.budgetCap != "" || f.runEstimate != ""
	if !given {
		return choice, false, nil
	}
	if f.budgetCap != "" {
		return f.cappedBudget()
	}
	if f.runEstimate != "" {
		return choice, true, usagef("--run-estimate needs --budget-cap")
	}
	if !f.confirmUnlimited {
		return choice, true, usagef("--alert-threshold and --no-alert need --confirm-unlimited or --budget-cap")
	}
	if f.noAlert == (f.alertThreshold != "") {
		return choice, true, usagef("--confirm-unlimited needs exactly one of --alert-threshold and --no-alert")
	}
	if f.noAlert {
		return install.BudgetChoice{Confirmed: true, AlertOff: true}, true, nil
	}
	choice, err = install.ParseAlert(f.alertThreshold)
	if err != nil {
		return choice, true, usagef("--alert-threshold: %v", err)
	}
	return choice, true, nil
}

// cappedBudget reads --budget-cap with its optional alert and run estimate.
func (f installFlags) cappedBudget() (install.BudgetChoice, bool, error) {
	choice := install.BudgetChoice{Confirmed: true}
	switch {
	case f.confirmUnlimited:
		return choice, true, usagef("--budget-cap and --confirm-unlimited are two different budgets; choose one")
	case f.noAlert:
		return choice, true, usagef("--no-alert is for the unlimited budget; a capped budget without --alert-threshold has no alert")
	}
	var err error
	if choice.CapMicros, err = install.ParseAmount(f.budgetCap); err != nil {
		return choice, true, usagef("--budget-cap: %v", err)
	}
	if f.alertThreshold != "" {
		if choice.AlertThresholdMicros, err = install.ParseAmount(f.alertThreshold); err != nil {
			return choice, true, usagef("--alert-threshold: %v", err)
		}
	}
	if f.runEstimate != "" {
		if choice.RunEstimateMicros, err = install.ParseAmount(f.runEstimate); err != nil {
			return choice, true, usagef("--run-estimate: %v", err)
		}
	}
	if err := choice.Validate(); err != nil {
		return choice, true, usagef("%v", err)
	}
	return choice, true, nil
}
