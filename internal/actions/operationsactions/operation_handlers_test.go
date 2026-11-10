package operationsactions

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// fakeSupervisor records what the actions asked of the admin socket.
type fakeSupervisor struct {
	status    supervisor.Status
	statusErr error
	stopErr   error
	stops     []supervisor.EmergencyStopRequest
	rollbacks []supervisor.RollbackRequest
}

func (f *fakeSupervisor) Status(context.Context) (supervisor.Status, error) {
	return f.status, f.statusErr
}

func (f *fakeSupervisor) EmergencyStop(_ context.Context, r supervisor.EmergencyStopRequest) (supervisor.EmergencyStopReport, error) {
	f.stops = append(f.stops, r)
	return supervisor.EmergencyStopReport{StopID: "stop-1", PublishingFrozen: true}, f.stopErr
}

func (f *fakeSupervisor) RequestRollback(_ context.Context, r supervisor.RollbackRequest) (supervisor.RollbackReport, error) {
	f.rollbacks = append(f.rollbacks, r)
	return supervisor.RollbackReport{Unit: r.Unit, RestoredTo: "sha256:lkg", Healthy: true}, nil
}

func withLKG(units map[manifest.UnitName]core.Digest) supervisor.Status {
	return supervisor.Status{FactoryID: "acme-app", LKG: supervisor.LKGStatus{Recorded: len(units) > 0, Units: units}}
}

func owner() core.Actor {
	return core.Actor{ID: "user:owner", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner}}
}

func registered(t *testing.T, sup Supervisor) *actions.Registry {
	t.Helper()
	reg := actions.NewRegistry()
	if err := Register(reg, Deps{FactoryID: "acme-app", Supervisor: sup}); err != nil {
		t.Fatal(err)
	}
	return reg
}

func call(t *testing.T, reg *actions.Registry, name string, req core.ActionRequest) (core.ActionResult, error) {
	t.Helper()
	a, ok := reg.Lookup(name)
	if !ok {
		t.Fatalf("%s is not registered", name)
	}
	req.Actor = owner()
	return a.Handle(context.Background(), req)
}

func TestRegisterRefusesAMissingSupervisor(t *testing.T) {
	t.Parallel()
	if err := Register(actions.NewRegistry(), Deps{FactoryID: "acme-app"}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("got %v", err)
	}
}

func TestEveryOperationsActionDeclaresScopes(t *testing.T) {
	t.Parallel()
	for _, s := range Specs() {
		if len(s.Scopes) == 0 || len(s.Roles) == 0 {
			t.Errorf("%s declares no scopes or roles", s.Name)
		}
	}
}

func TestTheStopDryRunAsksTheSupervisorNothing(t *testing.T) {
	t.Parallel()
	sup := &fakeSupervisor{}
	res, err := call(t, registered(t, sup), ActionEmergencyStop, core.ActionRequest{DryRun: true, Input: json.RawMessage(`{"reason":"bad deploy"}`)})
	if err != nil || res.Plan == nil || len(sup.stops) != 0 {
		t.Fatalf("plan %v, err %v, stops %d", res.Plan, err, len(sup.stops))
	}
}

func TestTheStopIsCarriedOutByTheSupervisor(t *testing.T) {
	t.Parallel()
	sup := &fakeSupervisor{}
	res, err := call(t, registered(t, sup), ActionEmergencyStop, core.ActionRequest{Input: json.RawMessage(`{"reason":"bad deploy"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(sup.stops) != 1 || sup.stops[0].Actor != "user:owner" || sup.stops[0].Reason != "bad deploy" {
		t.Fatalf("stops %+v", sup.stops)
	}
	var report supervisor.EmergencyStopReport
	if err := json.Unmarshal(res.Output, &report); err != nil || report.StopID != "stop-1" {
		t.Fatalf("output %s: %v", res.Output, err)
	}
}

func TestAnUnreachableSupervisorIsUnavailable(t *testing.T) {
	t.Parallel()
	sup := &fakeSupervisor{stopErr: errors.New("dial unix: connection refused")}
	_, err := call(t, registered(t, sup), ActionEmergencyStop, core.ActionRequest{Input: json.RawMessage(`{"reason":"x"}`)})
	if core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("got %v", err)
	}
}

func TestATypedRefusalOfTheSupervisorIsKept(t *testing.T) {
	t.Parallel()
	sup := &fakeSupervisor{stopErr: core.NewError(core.CodeConflict, "already stopped")}
	_, err := call(t, registered(t, sup), ActionEmergencyStop, core.ActionRequest{Input: json.RawMessage(`{"reason":"x"}`)})
	if core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("got %v", err)
	}
}

func TestTheRollbackTargetComesFromTheSupervisorsLastKnownGood(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		units map[manifest.UnitName]core.Digest
		input string
		code  core.ErrorCode
	}{
		{"nothing recorded", nil, `{"reason":"r"}`, core.CodeReleaseHold},
		{"two units and none named", map[manifest.UnitName]core.Digest{"web": "sha256:a", "api": "sha256:b"}, `{"reason":"r"}`, core.CodeInvalidArgument},
		{"an unknown unit", map[manifest.UnitName]core.Digest{"web": "sha256:a"}, `{"reason":"r","unit":"db"}`, core.CodeNotFound},
		{"no reason", map[manifest.UnitName]core.Digest{"web": "sha256:a"}, `{}`, core.CodeInvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			sup := &fakeSupervisor{status: withLKG(c.units)}
			_, err := call(t, registered(t, sup), ActionRollback, core.ActionRequest{DryRun: true, Input: json.RawMessage(c.input)})
			if core.CodeOf(err) != c.code || len(sup.rollbacks) != 0 {
				t.Fatalf("got %v, rollbacks %d", err, len(sup.rollbacks))
			}
		})
	}
}

func TestTheRollbackCarriesTheApprovalToTheSupervisor(t *testing.T) {
	t.Parallel()
	sup := &fakeSupervisor{status: withLKG(map[manifest.UnitName]core.Digest{"web": "sha256:lkg"})}
	reg := registered(t, sup)
	if _, err := call(t, reg, ActionRollback, core.ActionRequest{DryRun: true, Input: json.RawMessage(`{"reason":"r"}`)}); err != nil {
		t.Fatal(err)
	}
	if len(sup.rollbacks) != 0 {
		t.Fatal("a dry run asked for a rollback")
	}
	_, err := call(t, reg, ActionRollback, core.ActionRequest{ApprovalID: "apr-1", Input: json.RawMessage(`{"reason":"bad deploy"}`)})
	if err != nil {
		t.Fatal(err)
	}
	want := supervisor.RollbackRequest{FactoryID: "acme-app", Unit: "web", Reason: "bad deploy", ApprovalRef: "apr-1"}
	if len(sup.rollbacks) != 1 || sup.rollbacks[0] != want {
		t.Fatalf("rollbacks %+v", sup.rollbacks)
	}
}

func TestTheRollbackSubjectIsTheOneTheSupervisorChecks(t *testing.T) {
	t.Parallel()
	sup := &fakeSupervisor{status: withLKG(map[manifest.UnitName]core.Digest{"web": "sha256:lkg"})}
	h := handlers{Deps{FactoryID: "acme-app", Supervisor: sup}}
	got, err := h.rollbackSubject(context.Background(), json.RawMessage(`{"reason":"r"}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := release.RollbackSubject("acme-app", "web", "sha256:lkg"); got != want {
		t.Fatalf("subject %s, want %s", got, want)
	}
}
