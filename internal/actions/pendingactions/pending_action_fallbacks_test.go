package pendingactions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/actions/pendingactions"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestFallbacksRefuseHonestlyWithNextActions(t *testing.T) {
	t.Parallel()
	fallbacks := pendingactions.Fallbacks()
	if len(fallbacks) != len(pendingactions.Specs()) {
		t.Fatal("every spec needs a fallback")
	}
	reg := actions.NewRegistry()
	if err := reg.RegisterFallback(fallbacks...); err != nil {
		t.Fatalf("fallbacks must be valid registrations: %v", err)
	}
	for _, a := range fallbacks {
		_, err := a.Handle(context.Background(), core.ActionRequest{})
		var typed *core.Error
		if !errors.As(err, &typed) || typed.Code != core.CodeNotSupported || len(typed.NextActions) == 0 {
			t.Errorf("%s: error = %v, want NOT_SUPPORTED with next_actions", a.Spec.Name, err)
		}
		if !reg.IsFallback(a.Spec.Name) {
			t.Errorf("%s is not registered as a fallback", a.Spec.Name)
		}
	}
}

func TestSensitiveFallbacksDemandStepUp(t *testing.T) {
	t.Parallel()
	for _, spec := range pendingactions.Specs() {
		if spec.Sensitivity == core.SensitivitySensitive && !spec.StepUp {
			t.Errorf("%s is sensitive but asks for no step-up", spec.Name)
		}
	}
}
