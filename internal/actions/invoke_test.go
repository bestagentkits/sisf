package actions_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

func TestUnknownActionIsNotFoundWithNeighbours(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.svc.Invoke(context.Background(), "widgets.explode", core.ActionRequest{Actor: viewer()})
	typed := asError(t, err, core.CodeNotFound)
	if typed.NextActions[0].Action != "widgets.broken" {
		t.Fatalf("next_actions should point at registered actions of the same area, got %+v", typed.NextActions)
	}
	if f.authz.calls != 0 {
		t.Fatal("an unknown action must not reach the authorizer")
	}
}

func TestUnidentifiedActorIsUnauthenticated(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for name, actor := range map[string]core.Actor{
		"no id":           {Kind: core.ActorHuman, Surface: core.SurfaceAPI, Roles: []core.Role{core.RoleViewer}},
		"unknown kind":    {ID: "u", Kind: "browser", Surface: core.SurfaceAPI, Roles: []core.Role{core.RoleViewer}},
		"unknown surface": {ID: "u", Kind: core.ActorHuman, Surface: "email", Roles: []core.Role{core.RoleViewer}},
	} {
		_, err := f.svc.Invoke(context.Background(), "widgets.get", core.ActionRequest{Actor: actor})
		if !core.HasCode(err, core.CodeUnauthenticated) {
			t.Errorf("%s: error = %v, want UNAUTHENTICATED", name, err)
		}
	}
}

func TestEveryActionPassesThroughTheAuthorizer(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	res, err := f.svc.Invoke(context.Background(), "widgets.get", core.ActionRequest{Actor: viewer()})
	if err != nil || !strings.Contains(string(res.Output), "alpha") {
		t.Fatalf("read = %s, %v", res.Output, err)
	}
	if f.authz.calls != 1 {
		t.Fatalf("authorizer calls = %d, want 1", f.authz.calls)
	}

	reporter := viewer()
	reporter.Roles = []core.Role{core.RoleReporter}
	_, err = f.svc.Invoke(context.Background(), "widgets.get", core.ActionRequest{Actor: reporter})
	requireCode(t, err, core.CodePermissionDenied)
	if len(f.audit.Find("widgets.get", core.OutcomeDenied)) != 1 {
		t.Fatal("a denial must be audited")
	}

	f.authz.set("widgets.get", core.EffectRequireStepUp)
	_, err = f.svc.Invoke(context.Background(), "widgets.get", core.ActionRequest{Actor: viewer()})
	requireCode(t, err, core.CodeStepUpRequired)

	f.authz.set("widgets.get", core.EffectRequireApproval)
	_, err = f.svc.Invoke(context.Background(), "widgets.get", core.ActionRequest{Actor: viewer()})
	requireCode(t, err, core.CodePermissionDenied)

	f.authz.set("widgets.get", "maybe")
	_, err = f.svc.Invoke(context.Background(), "widgets.get", core.ActionRequest{Actor: viewer()})
	requireCode(t, err, core.CodePermissionDenied)
}

func TestAuthorizerFailureDenies(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.authz.err = errors.New("rbac backend down")
	_, err := f.svc.Invoke(context.Background(), "widgets.get", core.ActionRequest{Actor: viewer()})
	typed := asError(t, err, core.CodePermissionDenied)
	if strings.Contains(typed.Message, "rbac") {
		t.Fatal("the cause must not leak into the message")
	}
}

func TestUntypedHandlerFailureBecomesInternalWithoutLeaking(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.svc.Invoke(context.Background(), "widgets.broken", core.ActionRequest{Actor: viewer()})
	typed := asError(t, err, core.CodeInternal)
	if strings.Contains(typed.Message, "/var/lib") {
		t.Fatalf("message leaks the cause: %q", typed.Message)
	}
}

func TestMalformedInputIsInvalidArgument(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, in := range []string{`[1]`, `{"id":`, `"text"`, `{"id":"w1","extra":1}`} {
		_, err := f.svc.Invoke(context.Background(), "widgets.rename", core.ActionRequest{Actor: f.maintainer(), Input: input(in)})
		requireCode(t, err, core.CodeInvalidArgument)
	}
}

func TestPlainWriteRunsOnceAndIsAudited(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	res, err := f.svc.Invoke(context.Background(), "widgets.rename",
		core.ActionRequest{Actor: f.maintainer(), Input: input(`{"id":"w1","name":"beta"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if names, applied := f.widgets.snapshot(); names["w1"] != "beta" || applied != 1 {
		t.Fatalf("widgets = %v after %d changes", names, applied)
	}
	executed := f.audit.Find("widgets.rename", core.OutcomeExecuted)
	if len(executed) != 1 || res.AuditID == "" {
		t.Fatalf("executed records = %d, audit id %q", len(executed), res.AuditID)
	}
	if executed[0].ActorID != "u-maint" || executed[0].Surface != core.SurfaceCLI || executed[0].FactoryID != testFactory {
		t.Fatalf("audit record does not name the actor: %+v", executed[0])
	}
}

func TestDomainErrorKeepsItsCodeAndGainsNextActions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.svc.Invoke(context.Background(), "widgets.rename",
		core.ActionRequest{Actor: f.maintainer(), Input: input(`{"id":"nope","name":"x"}`)})
	requireCode(t, err, core.CodeNotFound)
	if len(f.audit.Find("widgets.rename", core.OutcomeFailed)) != 1 {
		t.Fatal("a failed write must be audited")
	}
}
