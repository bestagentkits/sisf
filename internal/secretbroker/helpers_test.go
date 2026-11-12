package secretbroker_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/internal/secretbroker"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/secrets"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

var start = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

// The secrets the fake provider holds. Values are made up per test run.
var (
	modelKey  = secrets.SecretRef{Provider: "local", Name: "providers/anthropic", Class: secrets.ClassRun, Zone: core.ZoneAgent}
	githubApp = secrets.SecretRef{Provider: "local", Name: "github/app-private-key", Class: secrets.ClassControl, Zone: core.ZoneControl}
	owner     = secrets.SecretRef{Provider: "local", Name: "owner/token", Class: secrets.ClassControl, Zone: core.ZoneControl}
	signing   = secrets.SecretRef{Provider: "local", Name: "signing/approval-key", Class: secrets.ClassSigning, Zone: core.ZoneControl}
	// controlRun is a run-class secret that belongs to the control zone.
	controlRun = secrets.SecretRef{Provider: "local", Name: "control/scratch", Class: secrets.ClassRun, Zone: core.ZoneControl}
)

// provider is an in-memory secret provider.
type provider struct{ values map[string]string }

func newProvider() *provider {
	return &provider{values: map[string]string{
		modelKey.String(): "model-key-value-0001", githubApp.String(): "github-app-key-value-0001",
		owner.String(): "owner-token-value-0001", signing.String(): "signing-key-value-0001",
		controlRun.String(): "control-scratch-value-0001",
	}}
}

func (p *provider) Resolve(_ context.Context, ref secrets.SecretRef) (core.SecretValue, error) {
	v, ok := p.values[ref.String()]
	if !ok {
		return core.SecretValue{}, core.NewError(core.CodeNotFound, "unknown secret")
	}
	return core.NewSecretValue([]byte(v)), nil
}

func (p *provider) ListRefs(context.Context) ([]secrets.SecretRef, error) {
	return []secrets.SecretRef{modelKey, githubApp, owner, signing, controlRun}, nil
}

type fixture struct {
	broker *secretbroker.Broker
	clock  *sqlstoretest.Clock
	audit  *intaketest.AuditLog
	values *provider
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	f := fixture{clock: sqlstoretest.NewClock(start), audit: &intaketest.AuditLog{}, values: newProvider()}
	b, err := secretbroker.New(secretbroker.Config{Provider: f.values, Clock: f.clock, MaxTTL: 30 * time.Minute,
		Audit: f.audit, FactoryID: "acme-app"})
	if err != nil {
		t.Fatal(err)
	}
	f.broker = b
	return f
}

func envGrant(run core.RunID, ref secrets.SecretRef, variable string) secrets.GrantRequest {
	return secrets.GrantRequest{RunID: run, Ref: ref, Purpose: secretbroker.PurposeSandboxEnv,
		Scopes: []string{secretbroker.EnvScope(variable)}, TTL: 10 * time.Minute}
}

func wantCode(t *testing.T, err error, code core.ErrorCode, what string) {
	t.Helper()
	if !core.HasCode(err, code) {
		t.Fatalf("%s: want %s, got %v", what, code, err)
	}
}

// assertNoValueLeaked fails when any secret value appears in an error or in
// the audit trail.
func (f fixture) assertNoValueLeaked(t *testing.T, errs ...error) {
	t.Helper()
	raw, err := json.Marshal(f.audit.Events())
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, e := range errs {
		if e != nil {
			text += e.Error()
		}
	}
	for _, v := range f.values.values {
		if strings.Contains(text, v) {
			t.Fatalf("a secret value leaked into an error or the audit trail")
		}
	}
}
