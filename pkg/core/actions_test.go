package core

import (
	"context"
	"slices"
	"testing"
)

func TestWorkspaceHasExactlySevenRoles(t *testing.T) {
	want := []Role{RoleOwner, RoleMaintainer, RoleReporter, RoleViewer, RoleAgent, RoleQA, RolePublisher}
	if got := Roles(); !slices.Equal(got, want) {
		t.Fatalf("Roles() = %v, want %v", got, want)
	}
	for _, r := range want {
		if !r.Valid() {
			t.Errorf("role %q is listed but not valid", r)
		}
	}
	for _, r := range []Role{"", "admin", "Owner", "sisf:approved"} {
		if r.Valid() {
			t.Errorf("role %q must not be valid", r)
		}
	}
}

func TestActorHoldsOnlyTheRolesItWasGiven(t *testing.T) {
	reporter := Actor{ID: "u-1", Kind: ActorHuman, Roles: []Role{RoleReporter}}
	if !reporter.HasRole(RoleReporter) || reporter.HasRole(RoleOwner) {
		t.Error("a reporter must not be treated as the Owner")
	}
	if (Actor{}).HasRole(RoleViewer) {
		t.Error("an actor without roles holds none")
	}
	for _, k := range []ActorKind{ActorHuman, ActorAgent, ActorService} {
		if !k.Valid() {
			t.Errorf("actor kind %q must be valid", k)
		}
	}
	if ActorKind("browser").Valid() || ActorKind("").Valid() {
		t.Error("unknown actor kinds must be rejected")
	}
}

func TestControlSurfacesAreExactlyFive(t *testing.T) {
	want := []Surface{SurfaceDashboard, SurfaceAPI, SurfaceCLI, SurfaceMCP, SurfaceWebMCP}
	if got := Surfaces(); !slices.Equal(got, want) {
		t.Fatalf("Surfaces() = %v, want %v", got, want)
	}
	if Surface("email").Valid() || !SurfaceWebMCP.Valid() {
		t.Error("surface validation is wrong")
	}
}

func TestAuthorizationDeniesUnlessExplicitlyAllowed(t *testing.T) {
	if (AuthzDecision{}).Allowed() {
		t.Fatal("the zero decision must deny")
	}
	for _, e := range []AuthzEffect{EffectDeny, EffectRequireApproval, EffectRequireStepUp, "ALLOW", "yes"} {
		if (AuthzDecision{Effect: e}).Allowed() {
			t.Errorf("effect %q must not allow", e)
		}
	}
	if !(AuthzDecision{Effect: EffectAllow}).Allowed() {
		t.Error("an explicit allow must allow")
	}
}

func validSpec() ActionSpec {
	return ActionSpec{
		Name:        "approvals.decide",
		Summary:     "Approve, reject or request changes on a proposal.",
		Sensitivity: SensitivitySensitive,
		Scopes:      []string{"approvals:write"},
		Roles:       []Role{RoleOwner},
		StepUp:      true,
	}
}

func TestActionSpecValidation(t *testing.T) {
	if err := validSpec().Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	cases := map[string]func(*ActionSpec){
		"unqualified name":    func(s *ActionSpec) { s.Name = "status" },
		"uppercase name":      func(s *ActionSpec) { s.Name = "Budget.Set" },
		"unknown sensitivity": func(s *ActionSpec) { s.Sensitivity = "public" },
		"no roles":            func(s *ActionSpec) { s.Roles = nil },
		"unknown role":        func(s *ActionSpec) { s.Roles = []Role{"admin"} },
		"step-up on a read":   func(s *ActionSpec) { s.Sensitivity = SensitivityRead },
	}
	for name, mutate := range cases {
		spec := validSpec()
		mutate(&spec)
		if CodeOf(spec.Validate()) != CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, CodeInvalidArgument, spec.Validate())
		}
	}
}

func TestOnlyReadsAreNonMutatingAndOnlySensitiveWritesNeedApproval(t *testing.T) {
	if (ActionSpec{Sensitivity: SensitivityRead}).Mutates() {
		t.Error("a read must not mutate")
	}
	for _, s := range []Sensitivity{SensitivityWrite, SensitivitySensitive} {
		if !(ActionSpec{Sensitivity: s}).Mutates() {
			t.Errorf("%q must count as a write", s)
		}
	}
	if !SensitivitySensitive.NeedsApproval() || SensitivityWrite.NeedsApproval() || SensitivityRead.NeedsApproval() {
		t.Error("only sensitive writes go through plan, approval, execute, audit")
	}
	if Sensitivity("").Valid() {
		t.Error("an unset sensitivity must be rejected")
	}
}

// denyAll proves the Authorizer contract can be met with the exported types alone.
type denyAll struct{}

func (denyAll) Authorize(context.Context, AuthzRequest) (AuthzDecision, error) {
	return AuthzDecision{Effect: EffectDeny, Reason: "no grant"}, nil
}

func TestActionPairsASpecWithAHandlerBehindTheAuthorizer(t *testing.T) {
	var authz Authorizer = denyAll{}
	action := Action{
		Spec: validSpec(),
		Handle: func(_ context.Context, req ActionRequest) (ActionResult, error) {
			if req.DryRun {
				return ActionResult{Plan: &DryRun{RequiresApproval: true,
					Changes: []Change{{Resource: "approval/1", Op: ChangeUpdate}}}}, nil
			}
			return ActionResult{}, NewError(CodeApprovalRequired, "needs the Owner")
		},
	}
	decision, err := authz.Authorize(context.Background(), AuthzRequest{Action: action.Spec})
	if err != nil || decision.Allowed() {
		t.Fatalf("decision = %+v, %v", decision, err)
	}
	res, err := action.Handle(context.Background(), ActionRequest{DryRun: true})
	if err != nil || res.Plan == nil || len(res.Plan.Changes) != 1 {
		t.Fatalf("dry run = %+v, %v", res, err)
	}
	if !ChangeCreate.Valid() || !ChangeDelete.Valid() || ChangeOp("patch").Valid() {
		t.Error("change operation validation is wrong")
	}
}
