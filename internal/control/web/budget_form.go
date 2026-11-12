package web

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// actionBudgetSet changes the budget. It needs step-up, so the dashboard's
// form is the only way a person runs it.
const actionBudgetSet = "budget.set"

// budgetAmounts are the amount fields of the budget form, in millionths of
// a currency unit.
var budgetAmounts = []string{"cap_micros", "alert_threshold_micros", "rollback_reserve_micros"}

// budgetInput builds a budget change from the security page's form. Only
// the fields the person filled in are sent; the action checks the rest.
func budgetInput(f url.Values) (any, error) {
	in := map[string]any{}
	switch mode := f.Get("mode"); mode {
	case string(core.BudgetUnlimited), string(core.BudgetCapped):
		in["mode"] = mode
	default:
		return nil, core.NewError(core.CodeInvalidArgument, "choose unlimited or capped")
	}
	for _, key := range budgetAmounts {
		raw := strings.TrimSpace(f.Get(key))
		if raw == "" {
			continue
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			return nil, core.NewError(core.CodeInvalidArgument, key+" must be a whole number of millionths, zero or more")
		}
		in[key] = n
	}
	switch period := f.Get("period"); period {
	case "":
	case "day", "week", "month":
		in["period"] = period
	default:
		return nil, core.NewError(core.CodeInvalidArgument, "the period is day, week or month")
	}
	if f.Get("provider_hard_cap") != "" {
		in["provider_hard_cap"] = true
	}
	return in, nil
}
