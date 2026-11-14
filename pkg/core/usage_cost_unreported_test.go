package core

import "testing"

// Usage counts as without a reported cost when it is unverified and states
// no cost, whatever tokens it counted: a row that prices nothing never
// proves nothing was spent, and token counts so large that their sum would
// not fit in an int64 change nothing.
func TestUsageWithoutAReportedCost(t *testing.T) {
	t.Parallel()
	const huge = int64(1) << 62
	for name, c := range map[string]struct {
		u    Usage
		want bool
	}{
		"tokens, no cost":     {Usage{InputTokens: 10, OutputTokens: 20}, true},
		"output tokens only":  {Usage{OutputTokens: 1}, true},
		"input tokens only":   {Usage{InputTokens: 1}, true},
		"huge token counts":   {Usage{InputTokens: huge, OutputTokens: huge}, true},
		"no tokens":           {Usage{}, true},
		"a cost":              {Usage{InputTokens: 10, Cost: 1}, false},
		"verified, no cost":   {Usage{InputTokens: 10, Verified: true}, false},
		"huge tokens, a cost": {Usage{InputTokens: huge, OutputTokens: huge, Cost: 1}, false},
	} {
		if got := c.u.CostUnreported(); got != c.want {
			t.Errorf("%s: CostUnreported = %v, want %v", name, got, c.want)
		}
	}
}
