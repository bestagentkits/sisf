package conformance

import "github.com/bestagentkits/sisf/pkg/core"

// checkBudget requires the Owner's decision about the spending alert to be
// one decision. When the budget is unlimited the factory sets no monetary
// cap of its own; the alert is then the only thing that tells the Owner what
// is being spent, so it is either set or explicitly declined, never left
// out. In any mode an alert that is both set and declined is refused: one of
// the two settings is a mistake, and which one cannot be told.
func (c *checker) checkBudget() {
	budget := c.m.Budget
	if budget.AlertOff && budget.AlertThresholdMicros > 0 {
		c.add(RuleBudgetAlert, "budget.alert_off",
			"remove budget.alert_off to be notified at budget.alert_threshold_micros, or remove the threshold to have no alert",
			"budget.alert_off declines the alert that budget.alert_threshold_micros sets; the two contradict each other, so it is unknown whether the Owner is told what the factory spends")
		return
	}
	if budget.Mode != core.BudgetUnlimited || budget.AlertThresholdMicros > 0 || budget.AlertOff {
		return
	}
	c.add(RuleBudgetAlert, "budget.alert_threshold_micros",
		"set budget.alert_threshold_micros to the spending at which the Owner is notified, or set budget.alert_off: true to state that no alert is wanted",
		"budget.mode is unlimited, yet no alert threshold is set and budget.alert_off does not say that none is wanted; with no cap and no alert nothing tells the Owner what the factory spends")
}
