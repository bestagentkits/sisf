package conformance

import "github.com/bestagentkits/sisf/pkg/core"

// checkBudget requires the Owner's decision about the spending alert when
// the budget is unlimited. Unlimited means the factory sets no monetary cap
// of its own; the alert is then the only thing that tells the Owner what is
// being spent, so it is either set or explicitly declined, never left out.
func (c *checker) checkBudget() {
	budget := c.m.Budget
	if budget.Mode != core.BudgetUnlimited || budget.AlertThresholdMicros > 0 || budget.AlertOff {
		return
	}
	c.add(RuleBudgetAlert, "budget.alert_threshold_micros",
		"set budget.alert_threshold_micros to the spending at which the Owner is notified, or set budget.alert_off: true to state that no alert is wanted",
		"budget.mode is unlimited, yet no alert threshold is set and budget.alert_off does not say that none is wanted; with no cap and no alert nothing tells the Owner what the factory spends")
}
