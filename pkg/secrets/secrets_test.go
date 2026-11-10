package secrets_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/core"
	"github.com/bestagentkits/sisf/pkg/secrets"
)

func TestOnlyRunScopedSecretsMayReachARun(t *testing.T) {
	if !secrets.ClassRun.BrokerableToRun() {
		t.Fatal("a run-scoped secret may be brokered to its run")
	}
	for _, c := range []secrets.Class{secrets.ClassControl, secrets.ClassSigning, "", "unknown"} {
		if c.BrokerableToRun() {
			t.Errorf("class %q must never be handed to a run", c)
		}
	}
	for _, c := range []secrets.Class{secrets.ClassRun, secrets.ClassControl, secrets.ClassSigning} {
		if !c.Valid() {
			t.Errorf("class %q must be valid", c)
		}
	}
	if secrets.Class("").Valid() {
		t.Error("a secret without a class must be rejected")
	}
}

func validRef() secrets.SecretRef {
	return secrets.SecretRef{Provider: "local", Name: "providers/anthropic-api-key", Class: secrets.ClassRun, Zone: core.ZoneAgent}
}

func TestSecretRefValidationAndText(t *testing.T) {
	if err := validRef().Validate(); err != nil {
		t.Fatalf("valid ref rejected: %v", err)
	}
	if validRef().String() != "local:providers/anthropic-api-key" {
		t.Fatalf("String() = %q", validRef().String())
	}
	cases := map[string]func(*secrets.SecretRef){
		"no provider":     func(r *secrets.SecretRef) { r.Provider = "" },
		"no name":         func(r *secrets.SecretRef) { r.Name = "" },
		"path traversal":  func(r *secrets.SecretRef) { r.Name = "../zone3/root-key" },
		"absolute name":   func(r *secrets.SecretRef) { r.Name = "/etc/passwd" },
		"name with space": func(r *secrets.SecretRef) { r.Name = "api key" },
		"no class":        func(r *secrets.SecretRef) { r.Class = "" },
		"shared zone":     func(r *secrets.SecretRef) { r.Zone = core.ZoneShared },
		"no zone":         func(r *secrets.SecretRef) { r.Zone = "" },
	}
	for name, mutate := range cases {
		ref := validRef()
		mutate(&ref)
		if core.CodeOf(ref.Validate()) != core.CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, ref.Validate())
		}
	}
}

func TestGrantRequestRefusesControlSecretsAndOpenEndedGrants(t *testing.T) {
	good := secrets.GrantRequest{RunID: "run-0001", Ref: validRef(), Purpose: "model provider calls", TTL: 30 * time.Minute}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	appKey := good
	appKey.Ref = secrets.SecretRef{Provider: "local", Name: "github/app-private-key", Class: secrets.ClassControl, Zone: core.ZoneControl}
	if !core.HasCode(appKey.Validate(), core.CodePermissionDenied) {
		t.Fatalf("the GitHub App key must never be granted to a run, got %v", appKey.Validate())
	}
	signing := good
	signing.Ref = secrets.SecretRef{Provider: "local", Name: "signing/delegated", Class: secrets.ClassSigning, Zone: core.ZoneControl}
	if !core.HasCode(signing.Validate(), core.CodePermissionDenied) {
		t.Fatalf("a signing key must never be granted to a run, got %v", signing.Validate())
	}
	cases := map[string]func(*secrets.GrantRequest){
		"no run":     func(g *secrets.GrantRequest) { g.RunID = "" },
		"no purpose": func(g *secrets.GrantRequest) { g.Purpose = "" },
		"no expiry":  func(g *secrets.GrantRequest) { g.TTL = 0 },
		"bad ref":    func(g *secrets.GrantRequest) { g.Ref.Name = "" },
	}
	for name, mutate := range cases {
		req := good
		mutate(&req)
		if core.CodeOf(req.Validate()) != core.CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, req.Validate())
		}
	}
}

func TestGrantEndsAtItsExpiry(t *testing.T) {
	expires := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
	grant := secrets.Grant{ID: "g-1", RunID: "run-0001", Ref: validRef(), ExpiresAt: expires}
	if grant.Expired(expires.Add(-time.Second)) || !grant.Expired(expires) {
		t.Fatal("a grant is usable only before its expiry")
	}
}

// providerStub and brokerStub prove the contracts can be met with the exported types alone.
type providerStub struct{}

func (providerStub) Resolve(context.Context, secrets.SecretRef) (core.SecretValue, error) {
	return core.NewSecretValue([]byte("value")), nil
}
func (providerStub) ListRefs(context.Context) ([]secrets.SecretRef, error) {
	return []secrets.SecretRef{validRef()}, nil
}
func (providerStub) Put(context.Context, secrets.SecretRef, core.SecretValue) error { return nil }
func (providerStub) Delete(context.Context, secrets.SecretRef) error                { return nil }

type brokerStub struct{ revoked map[core.RunID]bool }

func (b brokerStub) Issue(_ context.Context, req secrets.GrantRequest) (secrets.Grant, error) {
	if err := req.Validate(); err != nil {
		return secrets.Grant{}, err
	}
	return secrets.Grant{ID: "g-1", RunID: req.RunID, Ref: req.Ref, Purpose: req.Purpose}, nil
}
func (b brokerStub) Redeem(context.Context, string) (core.SecretValue, error) {
	if b.revoked["run-0001"] {
		return core.SecretValue{}, core.NewError(core.CodeExpired, "grant revoked")
	}
	return core.NewSecretValue([]byte("value")), nil
}
func (b brokerStub) RevokeRun(_ context.Context, run core.RunID) error {
	b.revoked[run] = true
	return nil
}

func TestProviderAndBrokerContractsAreImplementable(t *testing.T) {
	var provider secrets.SecretProvider = providerStub{}
	var _ secrets.Writer = providerStub{}
	var broker secrets.Broker = brokerStub{revoked: map[core.RunID]bool{}}
	ctx := context.Background()
	refs, err := provider.ListRefs(ctx)
	if err != nil || len(refs) != 1 {
		t.Fatalf("ListRefs = %v, %v", refs, err)
	}
	grant, err := broker.Issue(ctx, secrets.GrantRequest{RunID: "run-0001", Ref: validRef(), Purpose: "tests", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.RevokeRun(ctx, grant.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Redeem(ctx, grant.ID); !core.HasCode(err, core.CodeExpired) {
		t.Fatalf("a revoked grant must not be redeemable, got %v", err)
	}
}
