package actions_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/pkg/core"
)

func readSpec(name string) core.ActionSpec {
	return core.ActionSpec{
		Name: name, Summary: "Reads something.", Sensitivity: core.SensitivityRead,
		Scopes: []string{"things:read"}, Roles: []core.Role{core.RoleViewer},
	}
}

func okHandler(_ context.Context, _ core.ActionRequest) (core.ActionResult, error) {
	return core.ActionResult{Output: json.RawMessage(`{"ok":true}`)}, nil
}

func TestRegistryRefusesMalformedRegistrations(t *testing.T) {
	t.Parallel()
	noScopes := readSpec("things.list")
	noScopes.Scopes = nil
	noSummary := readSpec("things.list")
	noSummary.Summary = " "
	badSchema := readSpec("things.list")
	badSchema.Input = json.RawMessage(`[1,2]`)
	cases := map[string]core.Action{
		"invalid name":       {Spec: readSpec("Things"), Handle: okHandler},
		"missing handler":    {Spec: readSpec("things.list")},
		"missing scopes":     {Spec: noScopes, Handle: okHandler},
		"missing summary":    {Spec: noSummary, Handle: okHandler},
		"non-object schema":  {Spec: badSchema, Handle: okHandler},
		"step-up on a write": {Spec: core.ActionSpec{Name: "a.b", Summary: "x", Sensitivity: core.SensitivityWrite, Scopes: []string{"s"}, Roles: []core.Role{core.RoleOwner}, StepUp: true}, Handle: okHandler},
	}
	for name, action := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			reg := actions.NewRegistry()
			if err := reg.Register(action); !core.HasCode(err, core.CodeInvalidArgument) {
				t.Fatalf("Register = %v, want INVALID_ARGUMENT", err)
			}
			if len(reg.Specs()) != 0 {
				t.Fatal("a refused registration must leave the registry empty")
			}
		})
	}
}

func TestRegistryRefusesDuplicateNamesAtomically(t *testing.T) {
	t.Parallel()
	reg := actions.NewRegistry()
	if err := reg.Register(core.Action{Spec: readSpec("things.list"), Handle: okHandler}); err != nil {
		t.Fatal(err)
	}
	err := reg.Register(core.Action{Spec: readSpec("things.get"), Handle: okHandler},
		core.Action{Spec: readSpec("things.list"), Handle: okHandler})
	if !core.HasCode(err, core.CodeAlreadyExists) {
		t.Fatalf("duplicate = %v, want ALREADY_EXISTS", err)
	}
	if _, ok := reg.Lookup("things.get"); ok {
		t.Fatal("a batch with a duplicate must register nothing")
	}
	err = reg.Register(core.Action{Spec: readSpec("other.get"), Handle: okHandler},
		core.Action{Spec: readSpec("other.get"), Handle: okHandler})
	if !core.HasCode(err, core.CodeAlreadyExists) {
		t.Fatalf("duplicate inside one batch = %v, want ALREADY_EXISTS", err)
	}
}

func TestRealRegistrationReplacesFallbackInEitherOrder(t *testing.T) {
	t.Parallel()
	fallback := core.Action{Spec: readSpec("things.list"), Handle: func(context.Context, core.ActionRequest) (core.ActionResult, error) {
		return core.ActionResult{}, core.NewError(core.CodeNotSupported, "not yet")
	}}
	implemented := core.Action{Spec: readSpec("things.list"), Handle: okHandler}

	first := actions.NewRegistry()
	if err := first.RegisterFallback(fallback); err != nil {
		t.Fatal(err)
	}
	if err := first.Register(implemented); err != nil {
		t.Fatalf("a real action must replace a fallback: %v", err)
	}
	second := actions.NewRegistry()
	if err := second.Register(implemented); err != nil {
		t.Fatal(err)
	}
	if err := second.RegisterFallback(fallback); err != nil {
		t.Fatalf("a fallback for a registered name is ignored, not refused: %v", err)
	}
	for _, reg := range []*actions.Registry{first, second} {
		got, ok := reg.Lookup("things.list")
		if !ok {
			t.Fatal("the action is missing")
		}
		res, err := got.Handle(context.Background(), core.ActionRequest{})
		if err != nil || string(res.Output) != `{"ok":true}` {
			t.Fatalf("the real handler must win, got %s, %v", res.Output, err)
		}
		if reg.IsFallback("things.list") {
			t.Fatal("the action must no longer be marked as a fallback")
		}
	}
}

func TestRegistrySpecsAreSortedByName(t *testing.T) {
	t.Parallel()
	reg := actions.NewRegistry()
	for _, name := range []string{"runs.list", "approvals.list", "factory.status"} {
		if err := reg.Register(core.Action{Spec: readSpec(name), Handle: okHandler}); err != nil {
			t.Fatal(err)
		}
	}
	var names []string
	for _, spec := range reg.Specs() {
		names = append(names, spec.Name)
	}
	if !slices.Equal(names, []string{"approvals.list", "factory.status", "runs.list"}) {
		t.Fatalf("Specs() names = %v", names)
	}
}
