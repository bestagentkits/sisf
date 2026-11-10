package control_test

import (
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestParseIssueQuery(t *testing.T) {
	t.Parallel()
	good := []struct {
		name   string
		params map[string][]string
		states []core.State
		limit  int
	}{
		{"nothing selects everything", nil, nil, 0},
		{"states repeat once", map[string][]string{"state": {"Ready", "Ready", "Blocked"}}, []core.State{core.StateReady, core.StateBlocked}, 0},
		{"limit at the maximum", map[string][]string{"limit": {"500"}}, nil, control.MaxIssueLimit},
	}
	for _, tc := range good {
		q, err := control.ParseIssueQuery(tc.params)
		if err != nil || !slices.Equal(q.States, tc.states) || q.Limit != tc.limit {
			t.Errorf("%s: got %+v, %v", tc.name, q, err)
		}
	}
	bad := map[string]map[string][]string{
		"unknown state":      {"state": {"Shipped"}},
		"lowercase state":    {"state": {"ready"}},
		"empty state":        {"state": {""}},
		"limit zero":         {"limit": {"0"}},
		"limit negative":     {"limit": {"-1"}},
		"limit over the max": {"limit": {"501"}},
		"limit not a number": {"limit": {"ten"}},
	}
	for name, params := range bad {
		if _, err := control.ParseIssueQuery(params); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: error = %v, want INVALID_ARGUMENT", name, err)
		}
	}
}
