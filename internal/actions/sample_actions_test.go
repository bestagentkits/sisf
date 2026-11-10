package actions_test

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/pkg/core"
)

// sampleActions is a small domain: a read, a plain write, a sensitive write
// open to the Owner and Maintainers, an Owner-only sensitive write and a read
// that fails without a typed error.
func sampleActions(w *widgets) []core.Action {
	return []core.Action{
		{Spec: core.ActionSpec{Name: "widgets.get", Summary: "Read the widgets.", Sensitivity: core.SensitivityRead,
			Scopes: []string{"widgets:read"}, Roles: []core.Role{core.RoleViewer, core.RoleOwner, core.RoleMaintainer}},
			Handle: func(context.Context, core.ActionRequest) (core.ActionResult, error) {
				names, _ := w.snapshot()
				return actions.JSONResult(names)
			}},
		{Spec: core.ActionSpec{Name: "widgets.rename", Summary: "Rename a widget.", Sensitivity: core.SensitivityWrite,
			Scopes: []string{"widgets:write"}, Roles: []core.Role{core.RoleOwner, core.RoleMaintainer}},
			Handle: w.change(false)},
		{Spec: core.ActionSpec{Name: "widgets.purge", Summary: "Delete a widget for good.", Sensitivity: core.SensitivitySensitive,
			Scopes: []string{"widgets:admin"}, Roles: []core.Role{core.RoleOwner, core.RoleMaintainer}, StepUp: true},
			Handle: w.change(true)},
		{Spec: core.ActionSpec{Name: "keys.rotate", Summary: "Rotate the signing key.", Sensitivity: core.SensitivitySensitive,
			Scopes: []string{"keys:admin"}, Roles: []core.Role{core.RoleOwner}, StepUp: true},
			Handle: w.change(true)},
		{Spec: core.ActionSpec{Name: "widgets.broken", Summary: "Always fails.", Sensitivity: core.SensitivityRead,
			Scopes: []string{"widgets:read"}, Roles: []core.Role{core.RoleViewer}},
			Handle: func(context.Context, core.ActionRequest) (core.ActionResult, error) {
				return core.ActionResult{}, errors.New("disk on fire at /var/lib/secret")
			}},
	}
}

type widgetInput struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// change renames a widget, or deletes it when purge is set. A dry run
// reports the change and touches nothing.
func (w *widgets) change(purge bool) core.ActionHandler {
	return func(_ context.Context, req core.ActionRequest) (core.ActionResult, error) {
		in, err := actions.DecodeInput[widgetInput](req)
		if err != nil {
			return core.ActionResult{}, err
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		before, ok := w.names[in.ID]
		if !ok {
			return core.ActionResult{}, core.NewError(core.CodeNotFound, "no such widget")
		}
		change := core.Change{Resource: "widget:" + in.ID, Op: core.ChangeUpdate,
			Before: mustJSON(before), After: mustJSON(in.Name)}
		if purge {
			change.Op, change.After = core.ChangeDelete, nil
		}
		if req.DryRun {
			return core.ActionResult{Plan: &core.DryRun{Changes: []core.Change{change}, Warnings: []string{"Removes widget " + in.ID + " for good."}}}, nil
		}
		if purge {
			delete(w.names, in.ID)
		} else {
			w.names[in.ID] = in.Name
		}
		w.applied++
		return actions.JSONResult(map[string]string{"id": in.ID})
	}
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}
