package actions_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// spender stands for the trusted component that verifies and spends a
// delegated approval in its own transaction.
type spender struct {
	store   *approvals.KVStore
	subject core.Digest
	spent   []string
}

func (s *spender) handle(ctx context.Context, req core.ActionRequest) (core.ActionResult, error) {
	if req.DryRun {
		return core.ActionResult{Plan: &core.DryRun{Changes: []core.Change{{Resource: "deployments/app", Op: core.ChangeUpdate}}}}, nil
	}
	if _, err := s.store.Consume(ctx, req.ApprovalID, s.subject, policyA); err != nil {
		return core.ActionResult{}, err
	}
	s.spent = append(s.spent, req.ApprovalID)
	return actions.JSONResult(map[string]bool{"restored": true})
}

func digestOf(text string) core.Digest {
	sum := sha256.Sum256([]byte(text))
	return core.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

// newConfigured builds a service over the sample actions with cfg edited.
func newConfigured(t *testing.T, edit func(*actions.Config, *actions.Registry, *approvals.KVStore)) (fixture, *actions.Registry) {
	t.Helper()
	f := newFixture(t)
	reg := actions.NewRegistry()
	if err := reg.Register(sampleActions(f.widgets)...); err != nil {
		t.Fatal(err)
	}
	cfg := actions.Config{FactoryID: testFactory, Registry: reg, Authorizer: f.authz, Approvals: f.approvals,
		Policy: f.policy, Audit: f.audit, Clock: f.clock}
	edit(&cfg, reg, f.approvals)
	svc, err := actions.NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.svc = svc
	return f, reg
}

func TestResolvedRolesReplaceTheRolesACallerClaims(t *testing.T) {
	t.Parallel()
	f, _ := newConfigured(t, func(cfg *actions.Config, _ *actions.Registry, _ *approvals.KVStore) {
		cfg.Roles = actions.RoleResolverFunc(func(_ context.Context, a core.Actor) ([]core.Role, error) {
			if a.ID == "u-owner" {
				return []core.Role{core.RoleOwner}, nil
			}
			return nil, nil
		})
	})
	claimed := viewer()
	claimed.Roles = []core.Role{core.RoleOwner}
	_, err := f.svc.Invoke(context.Background(), "widgets.purge", core.ActionRequest{Actor: claimed, Input: input(purgeW1)})
	requireCode(t, err, core.CodePermissionDenied)
	unclaimed := f.owner()
	unclaimed.Roles = nil
	if id := f.plan(t, unclaimed, "widgets.purge", purgeW1); id == "" {
		t.Fatal("the resolved Owner could not plan")
	}
}

func TestAFailedRoleResolutionRunsNothing(t *testing.T) {
	t.Parallel()
	f, _ := newConfigured(t, func(cfg *actions.Config, _ *actions.Registry, _ *approvals.KVStore) {
		cfg.Roles = actions.RoleResolverFunc(func(context.Context, core.Actor) ([]core.Role, error) {
			return nil, core.NewError(core.CodeUnavailable, "github is down")
		})
	})
	_, err := f.svc.Invoke(context.Background(), "widgets.get", core.ActionRequest{Actor: f.owner()})
	requireCode(t, err, core.CodeUnavailable)
}

func TestWritesBeyondTheRateLimitAreRefusedAndReadsAreNot(t *testing.T) {
	t.Parallel()
	f, _ := newConfigured(t, func(cfg *actions.Config, _ *actions.Registry, _ *approvals.KVStore) {
		cfg.RateLimit = actions.RateLimit{Writes: 2, Window: time.Minute}
	})
	ctx := context.Background()
	rename := func() error {
		_, err := f.svc.Invoke(ctx, "widgets.rename", core.ActionRequest{Actor: f.maintainer(), Input: input(`{"id":"w1","name":"beta"}`)})
		return err
	}
	for i := range 2 {
		if err := rename(); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	err := rename()
	typed := asError(t, err, core.CodeRateLimited)
	if typed.Details["retry_after_seconds"] == "" {
		t.Fatalf("the refusal does not say when to retry: %+v", typed)
	}
	if _, err := f.svc.Invoke(ctx, "widgets.get", core.ActionRequest{Actor: f.maintainer()}); err != nil {
		t.Fatalf("a read was limited: %v", err)
	}
	if _, err := f.svc.Invoke(ctx, "widgets.rename", core.ActionRequest{Actor: f.owner(), Input: input(`{"id":"w1","name":"c"}`)}); err != nil {
		t.Fatalf("another actor shares the limit: %v", err)
	}
	f.clock.Advance(time.Minute)
	if err := rename(); err != nil {
		t.Fatalf("the window did not end: %v", err)
	}
}

func TestADelegatedApprovalIsSpentByThePerformerOnlyOnce(t *testing.T) {
	t.Parallel()
	subject := digestOf("restore app to lkg-1")
	var sp *spender
	f, _ := newConfigured(t, func(cfg *actions.Config, reg *actions.Registry, store *approvals.KVStore) {
		sp = &spender{store: store, subject: subject}
		spec := core.ActionSpec{Name: "deployments.restore", Summary: "Restore.", Sensitivity: core.SensitivitySensitive,
			Scopes: []string{"deployments:write"}, Roles: []core.Role{core.RoleOwner, core.RoleMaintainer}, StepUp: true}
		err := reg.RegisterDelegated(actions.DelegatedApproval{Action: core.Action{Spec: spec, Handle: sp.handle},
			Subject: func(context.Context, json.RawMessage) (core.Digest, error) { return subject, nil }})
		if err != nil {
			t.Fatal(err)
		}
	})
	id := f.plan(t, f.maintainer(), "deployments.restore", `{"reason":"bad"}`)
	if r, _ := f.approvals.Get(context.Background(), id); r.SubjectDigest != subject {
		t.Fatalf("the request is bound to %s, want the performer's subject", r.SubjectDigest)
	}
	_, err := f.execute(f.maintainer(), "deployments.restore", `{"reason":"bad"}`, id)
	requireCode(t, err, core.CodeApprovalRequired)
	f.approve(t, id, approvals.DecisionApprove)
	if _, err := f.execute(f.maintainer(), "deployments.restore", `{"reason":"bad"}`, id); err != nil {
		t.Fatalf("the approved call failed: %v", err)
	}
	_, err = f.execute(f.maintainer(), "deployments.restore", `{"reason":"bad"}`, id)
	requireCode(t, err, core.CodeReplayed)
	if len(sp.spent) != 1 {
		t.Fatalf("the performer spent %v, want exactly one approval", sp.spent)
	}
}

func TestADelegatedActionMustBeSensitiveAndNameItsSubject(t *testing.T) {
	t.Parallel()
	reg := actions.NewRegistry()
	spec := core.ActionSpec{Name: "deployments.restore", Summary: "Restore.", Sensitivity: core.SensitivityWrite,
		Scopes: []string{"deployments:write"}, Roles: []core.Role{core.RoleOwner}}
	handle := func(context.Context, core.ActionRequest) (core.ActionResult, error) { return core.ActionResult{}, nil }
	if err := reg.RegisterDelegated(actions.DelegatedApproval{Action: core.Action{Spec: spec, Handle: handle},
		Subject: func(context.Context, json.RawMessage) (core.Digest, error) { return digestOf("x"), nil }}); err == nil {
		t.Fatal("a plain write was accepted as delegated")
	}
	spec.Sensitivity, spec.StepUp = core.SensitivitySensitive, true
	if err := reg.RegisterDelegated(actions.DelegatedApproval{Action: core.Action{Spec: spec, Handle: handle}}); err == nil {
		t.Fatal("a delegated action without a subject was accepted")
	}
}
