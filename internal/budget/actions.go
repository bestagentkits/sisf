package budget

import (
	"context"
	"encoding/json"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Names of the budget actions.
const (
	ActionGet = "budget.get"
	ActionSet = "budget.set"
)

// ActionsConfig is what the budget actions are built from.
type ActionsConfig struct {
	Store Store
	// Confirmations reads the install-time confirmation; nil means the
	// entry the installer writes.
	Confirmations dispatcher.ConfirmationSource
	// Clock is the source of time; nil means the wall clock.
	Clock core.Clock
}

const (
	getInput = `{"type":"object","additionalProperties":false}`
	setInput = `{"type":"object","additionalProperties":false,"required":["mode"],"properties":{` +
		`"mode":{"enum":["unlimited","capped"]},"cap_micros":{"type":"integer","minimum":0},` +
		`"period":{"enum":["","day","week","month"]},"alert_threshold_micros":{"type":"integer","minimum":0},` +
		`"rollback_reserve_micros":{"type":"integer","minimum":0},"provider_hard_cap":{"type":"boolean"}}}`
	anyOutput = `{"type":"object"}`
)

// Actions returns budget.get, which reads, and budget.set, which is
// sensitive: Owner only, with step-up and an approval, like every spending
// decision. They replace the stand-ins of the same names.
func Actions(cfg ActionsConfig) ([]core.Action, error) {
	if cfg.Store == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "the budget actions need a store")
	}
	if cfg.Confirmations == nil {
		cfg.Confirmations = dispatcher.KVConfirmation{}
	}
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	h := handlers{cfg}
	return []core.Action{
		{Spec: core.ActionSpec{Name: ActionGet, Summary: "Show the budget mode, cap, alert threshold and usage, with unverified usage marked.",
			Sensitivity: core.SensitivityRead, Scopes: []string{"budget:read"},
			Roles: []core.Role{core.RoleOwner, core.RoleMaintainer, core.RoleViewer},
			Input: actions.Schema(getInput), Output: actions.Schema(anyOutput)}, Handle: h.get},
		{Spec: core.ActionSpec{Name: ActionSet, Summary: "Change the budget mode, cap, alert threshold or rollback reserve; Owner only.",
			Sensitivity: core.SensitivitySensitive, Scopes: []string{"budget:write"}, Roles: []core.Role{core.RoleOwner}, StepUp: true,
			Input: actions.Schema(setInput), Output: actions.Schema(anyOutput)}, Handle: h.set},
	}, nil
}

type handlers struct{ cfg ActionsConfig }

func (h handlers) get(ctx context.Context, req core.ActionRequest) (core.ActionResult, error) {
	if _, err := actions.DecodeInput[struct{}](req); err != nil {
		return core.ActionResult{}, err
	}
	var st Status
	err := h.cfg.Store.View(ctx, func(ctx context.Context, l Ledger) (err error) {
		st, err = ReadStatus(ctx, h.cfg.Confirmations, l, h.cfg.Clock.Now())
		return err
	})
	if err != nil {
		return core.ActionResult{}, err
	}
	return actions.JSONResult(st)
}

// set stores new settings. A dry run reports the change and writes nothing;
// the approval pipeline plans every call this way before the Owner approves.
func (h handlers) set(ctx context.Context, req core.ActionRequest) (core.ActionResult, error) {
	next, err := actions.DecodeInput[Settings](req)
	if err != nil {
		return core.ActionResult{}, err
	}
	if err := next.Validate(); err != nil {
		return core.ActionResult{}, err
	}
	var plan core.DryRun
	err = h.cfg.Store.Update(ctx, func(ctx context.Context, l Ledger) error {
		current, version, found, err := loadSettings(ctx, l)
		if err != nil {
			return err
		}
		op := core.ChangeCreate
		if found {
			op = core.ChangeUpdate
		}
		before, _ := json.Marshal(current)
		after, _ := json.Marshal(next)
		plan = core.DryRun{Changes: []core.Change{{Resource: "budget", Op: op, Before: before, After: after}}, Warnings: warnings(current, found, next)}
		if req.DryRun {
			return nil
		}
		return saveSettings(ctx, l, next, version)
	})
	if err != nil {
		return core.ActionResult{}, err
	}
	if req.DryRun {
		return core.ActionResult{Plan: &plan}, nil
	}
	return actions.JSONResult(next)
}

// warnings names what a change would loosen, so the Owner approves it knowingly.
func warnings(current Settings, found bool, next Settings) []string {
	var out []string
	switch {
	case next.Mode == core.BudgetUnlimited && (!found || current.Mode == core.BudgetCapped):
		out = append(out, "Removes the monetary cap: the factory will meter spending but no longer stop it.")
	case next.Mode == core.BudgetCapped && found && next.CapMicros > current.CapMicros:
		out = append(out, "Raises the spending cap.")
	}
	if !next.ProviderHardCap {
		out = append(out, "The provider has no hard cap, so the budget is not a guarantee against charges.")
	}
	return out
}
