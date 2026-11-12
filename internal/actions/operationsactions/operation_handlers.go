package operationsactions

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Supervisor is the release supervisor's admin socket as these actions use
// it; *supervisor.Client is one.
type Supervisor interface {
	Status(ctx context.Context) (supervisor.Status, error)
	EmergencyStop(ctx context.Context, req supervisor.EmergencyStopRequest) (supervisor.EmergencyStopReport, error)
	RequestRollback(ctx context.Context, req supervisor.RollbackRequest) (supervisor.RollbackReport, error)
}

// Deps is what the actions are built from.
type Deps struct {
	FactoryID  core.FactoryID
	Supervisor Supervisor
}

// Register adds both actions to reg: the stop as a plain write, the rollback
// as a delegated sensitive action whose approval the supervisor spends.
func Register(reg *actions.Registry, deps Deps) error {
	if reg == nil || deps.Supervisor == nil {
		return core.NewError(core.CodeInvalidArgument, "the operations actions need a registry and the supervisor client")
	}
	if err := deps.FactoryID.Validate(); err != nil {
		return err
	}
	h := handlers{deps}
	specs := map[string]core.ActionSpec{}
	for _, s := range Specs() {
		specs[s.Name] = s
	}
	if err := reg.Register(core.Action{Spec: specs[ActionEmergencyStop], Handle: h.stop}); err != nil {
		return err
	}
	return reg.RegisterDelegated(actions.DelegatedApproval{
		Action: core.Action{Spec: specs[ActionRollback], Handle: h.rollback}, Subject: h.rollbackSubject,
	})
}

type handlers struct{ Deps }

type stopInputBody struct {
	Reason string `json:"reason"`
}

// stop asks the supervisor to stop the factory. A dry run names what a stop
// does and asks nothing.
func (h handlers) stop(ctx context.Context, req core.ActionRequest) (core.ActionResult, error) {
	in, err := actions.DecodeInput[stopInputBody](req)
	if err != nil {
		return core.ActionResult{}, err
	}
	stopReq := supervisor.EmergencyStopRequest{FactoryID: h.FactoryID, Actor: req.Actor.ID, Reason: strings.TrimSpace(in.Reason)}
	if err := stopReq.Validate(); err != nil {
		return core.ActionResult{}, err
	}
	if req.DryRun {
		return core.ActionResult{Plan: &core.DryRun{
			Changes:  []core.Change{{Resource: "factory/emergency_stop", Op: core.ChangeCreate}},
			Warnings: []string{"Stops claiming, cancels every running run, revokes their secrets and freezes publishing until the stop is lifted."},
		}}, nil
	}
	report, err := h.Supervisor.EmergencyStop(ctx, stopReq)
	if err != nil {
		return core.ActionResult{}, unreachable(err, "the release supervisor did not carry out the stop")
	}
	return actions.JSONResult(report)
}

type rollbackInputBody struct {
	Reason string `json:"reason"`
	Unit   string `json:"unit,omitempty"`
}

// target resolves the unit and its last known good release from the
// supervisor's own status.
func (h handlers) target(ctx context.Context, raw json.RawMessage) (rollbackInputBody, core.ReleaseDigest, error) {
	in, err := actions.DecodeInput[rollbackInputBody](core.ActionRequest{Input: raw})
	if err != nil {
		return in, "", err
	}
	in.Reason, in.Unit = strings.TrimSpace(in.Reason), strings.TrimSpace(in.Unit)
	status, err := h.Supervisor.Status(ctx)
	if err != nil {
		return in, "", unreachable(err, "the release supervisor could not say what to roll back to")
	}
	units := make([]string, 0, len(status.LKG.Units))
	for unit := range status.LKG.Units {
		units = append(units, string(unit))
	}
	sort.Strings(units)
	switch {
	case !status.LKG.Recorded || len(units) == 0:
		return in, "", core.NewError(core.CodeReleaseHold, "no last known good release is recorded, so there is nothing to roll back to",
			core.NextAction{Action: "runs.explain", Description: "see why no release was recorded"})
	case in.Unit == "" && len(units) == 1:
		in.Unit = units[0]
	case in.Unit == "":
		return in, "", core.NewError(core.CodeInvalidArgument, "name the unit to roll back: "+strings.Join(units, ", "))
	case !slices.Contains(units, in.Unit):
		return in, "", core.NewError(core.CodeNotFound, "unit "+in.Unit+" has no last known good release")
	}
	return in, core.ReleaseDigest(status.LKG.Units[manifest.UnitName(in.Unit)]), nil
}

// rollbackSubject binds the approval to restoring exactly this unit of this
// factory to its current last known good release, the subject the
// supervisor checks when it spends the approval.
func (h handlers) rollbackSubject(ctx context.Context, raw json.RawMessage) (core.Digest, error) {
	in, lkg, err := h.target(ctx, raw)
	if err != nil {
		return "", err
	}
	return release.RollbackSubject(h.FactoryID, in.Unit, lkg), nil
}

// rollback asks the supervisor to restore the unit under the approval the
// call carries. A dry run reports the target and changes nothing.
func (h handlers) rollback(ctx context.Context, req core.ActionRequest) (core.ActionResult, error) {
	in, lkg, err := h.target(ctx, req.Input)
	if err != nil {
		return core.ActionResult{}, err
	}
	if in.Reason == "" {
		return core.ActionResult{}, core.NewError(core.CodeInvalidArgument, "a rollback needs a reason")
	}
	if req.DryRun {
		after, _ := json.Marshal(map[string]string{"unit": in.Unit, "restore_to": string(lkg)})
		return core.ActionResult{Plan: &core.DryRun{
			Changes:  []core.Change{{Resource: "deployments/" + in.Unit, Op: core.ChangeUpdate, After: after}},
			Warnings: []string{"Restores " + in.Unit + " to its last known good release " + string(lkg) + "; data volumes are not touched."},
		}}, nil
	}
	report, err := h.Supervisor.RequestRollback(ctx, supervisor.RollbackRequest{
		FactoryID: h.FactoryID, Unit: in.Unit, Reason: in.Reason, ApprovalRef: req.ApprovalID,
	})
	if err != nil {
		return core.ActionResult{}, unreachable(err, "the release supervisor did not roll back")
	}
	return actions.JSONResult(report)
}

// unreachable keeps a typed refusal of the supervisor as it is and turns
// anything else into UNAVAILABLE.
func unreachable(err error, message string) error {
	if code := core.CodeOf(err); code != "" && code != core.CodeInternal {
		return err
	}
	return core.NewError(core.CodeUnavailable, message).WithCause(err)
}
