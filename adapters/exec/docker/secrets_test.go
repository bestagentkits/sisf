package docker_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/exec/docker"
	"github.com/bestagentkits/sisf/internal/secretbroker"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
	"github.com/bestagentkits/sisf/pkg/secrets"
)

var (
	modelKey = secrets.SecretRef{Provider: "local", Name: "providers/anthropic", Class: secrets.ClassRun, Zone: core.ZoneAgent}
	ownerTok = secrets.SecretRef{Provider: "local", Name: "owner/token", Class: secrets.ClassControl, Zone: core.ZoneControl}
	appKey   = secrets.SecretRef{Provider: "local", Name: "github/app-private-key", Class: secrets.ClassControl, Zone: core.ZoneControl}
)

// vault is an in-memory secret provider.
type vault map[string]string

func (v vault) Resolve(_ context.Context, ref secrets.SecretRef) (core.SecretValue, error) {
	value, ok := v[ref.String()]
	if !ok {
		return core.SecretValue{}, core.NewError(core.CodeNotFound, "unknown secret")
	}
	return core.NewSecretValue([]byte(value)), nil
}

func (v vault) ListRefs(context.Context) ([]secrets.SecretRef, error) {
	return []secrets.SecretRef{modelKey, ownerTok, appKey}, nil
}

func newBrokerEnv(t *testing.T) (env, *secretbroker.Broker, vault) {
	t.Helper()
	values := vault{modelKey.String(): "model-key-value-0001", ownerTok.String(): "owner-token-value-0001", appKey.String(): "app-key-value-0001"}
	var broker *secretbroker.Broker
	e := newEnv(t, options{cfg: func(c *docker.Config) {
		var err error
		broker, err = secretbroker.New(secretbroker.Config{Provider: values, Clock: c.Clock})
		if err != nil {
			t.Fatal(err)
		}
		c.Secrets = broker
	}})
	return e, broker, values
}

func TestARunsSecretsReachOnlyItsOwnSandbox(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e, broker, values := newBrokerEnv(t)
	grant, err := broker.Issue(ctx, secrets.GrantRequest{RunID: "run-0001", Ref: modelKey, Purpose: secretbroker.PurposeSandboxEnv,
		Scopes: []string{secretbroker.EnvScope("ANTHROPIC_API_KEY")}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	s := spec("run-0001", execution.TrustUntrusted)
	s.SecretGrantIDs = []string{grant.ID}
	sb := e.create(t, s)
	if _, err := e.backend.Exec(ctx, sb.ID, execution.Command{Argv: []string{"agent"}}); err != nil {
		t.Fatal(err)
	}
	if env := e.createdBodies(t)[0].Env; !slices.Contains(env, "ANTHROPIC_API_KEY=model-key-value-0001") {
		t.Fatal("the run's granted secret did not reach its sandbox")
	}

	other := spec("run-0002", execution.TrustUntrusted)
	other.SecretGrantIDs = []string{grant.ID}
	_, err = e.backend.CreateSandbox(ctx, other)
	wantCode(t, err, core.CodePermissionDenied, "another run's grant")

	push, err := broker.Issue(ctx, secrets.GrantRequest{RunID: "run-0001", Ref: modelKey, Purpose: "git.push", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	wrongPurpose := spec("run-0001", execution.TrustUntrusted)
	wrongPurpose.SecretGrantIDs = []string{push.ID}
	_, err = e.backend.CreateSandbox(ctx, wrongPurpose)
	wantCode(t, err, core.CodeInvalidArgument, "a grant made for another purpose")

	// Cancelling the run revokes its grants; its sandbox gets no more.
	if err := broker.RevokeRun(ctx, "run-0001"); err != nil {
		t.Fatal(err)
	}
	_, err = e.backend.Exec(ctx, sb.ID, execution.Command{Argv: []string{"agent"}})
	wantCode(t, err, core.CodeExpired, "a command after the run was cancelled")
	if n := len(e.daemon.Created()); n != 1 {
		t.Fatalf("%d containers were created, want only the first", n)
	}
	for _, r := range e.daemon.Requests() {
		for _, forbidden := range []string{values[ownerTok.String()], values[appKey.String()]} {
			if strings.Contains(string(r.Body), forbidden) || strings.Contains(r.Query, forbidden) {
				t.Fatal("a control-plane credential reached the daemon")
			}
		}
	}
}

// Neither the Owner token nor the GitHub App key can be granted, so no
// sandbox can be made to carry them.
func TestControlPlaneCredentialsCannotEnterASandbox(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, broker, _ := newBrokerEnv(t)
	for _, ref := range []secrets.SecretRef{ownerTok, appKey} {
		for _, claimed := range []secrets.SecretRef{ref, {Provider: ref.Provider, Name: ref.Name, Class: secrets.ClassRun, Zone: core.ZoneAgent}} {
			_, err := broker.Issue(ctx, secrets.GrantRequest{RunID: "run-0001", Ref: claimed, Purpose: secretbroker.PurposeSandboxEnv,
				Scopes: []string{secretbroker.EnvScope("GITHUB_TOKEN")}, TTL: time.Hour})
			wantCode(t, err, core.CodePermissionDenied, "granting "+ref.String())
		}
	}
}
