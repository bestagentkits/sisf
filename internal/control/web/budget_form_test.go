package web

import (
	"net/url"
	"reflect"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

func TestTheBudgetFormSendsOnlyWhatWasFilledIn(t *testing.T) {
	t.Parallel()
	got, err := budgetInput(url.Values{"mode": {"capped"}, "cap_micros": {"5000000"}, "period": {"month"}, "alert_threshold_micros": {""},
		"rollback_reserve_micros": {"0"}, "provider_hard_cap": {"1"}})
	want := map[string]any{"mode": "capped", "cap_micros": int64(5000000), "period": "month", "rollback_reserve_micros": int64(0),
		"provider_hard_cap": true}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("budgetInput = %v, %v", got, err)
	}
	for name, form := range map[string]url.Values{
		"no mode":         {},
		"unknown mode":    {"mode": {"free"}},
		"negative cap":    {"mode": {"capped"}, "cap_micros": {"-1"}},
		"fractional cap":  {"mode": {"capped"}, "cap_micros": {"1.5"}},
		"unknown period":  {"mode": {"capped"}, "period": {"year"}},
		"text in reserve": {"mode": {"capped"}, "rollback_reserve_micros": {"lots"}},
	} {
		if _, err := budgetInput(form); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
