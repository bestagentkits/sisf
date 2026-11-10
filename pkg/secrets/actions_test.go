package secrets_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/secrets"
	"github.com/bestagentkits/sisf/pkg/secrets/local"
)

const secretText = "zone3-test-value-that-must-never-leak"

var (
	webhookRef = secrets.SecretRef{Provider: local.ProviderName, Name: "github/webhook", Class: secrets.ClassControl, Zone: core.ZoneControl}
	runKeyRef  = secrets.SecretRef{Provider: local.ProviderName, Name: "agent/provider-key", Class: secrets.ClassRun, Zone: core.ZoneControl}
)

// resolveCounter wraps a provider and counts Resolve calls, so a test can
// prove the action never asked for a value.
type resolveCounter struct {
	secrets.SecretProvider
	resolves atomic.Int64
}

func (r *resolveCounter) Resolve(ctx context.Context, ref secrets.SecretRef) (core.SecretValue, error) {
	r.resolves.Add(1)
	return r.SecretProvider.Resolve(ctx, ref)
}

type failingProvider struct{ secrets.SecretProvider }

func (failingProvider) ListRefs(context.Context) ([]secrets.SecretRef, error) {
	return nil, errors.New("backend says " + secretText)
}

func filledStore(t *testing.T) *local.Store {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "control", "secret-store.key")
	if err := local.NewKeyFile(key, nil); err != nil {
		t.Fatal(err)
	}
	s, err := local.Open(local.Config{Path: filepath.Join(dir, "control", "secrets.sealed"), KeyFile: key, Zone: core.ZoneControl})
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []secrets.SecretRef{webhookRef, runKeyRef} {
		if err := s.Put(context.Background(), ref, core.NewSecretValue([]byte(secretText))); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func invoke(t *testing.T, providers ...secrets.SecretProvider) (core.ActionResult, error) {
	t.Helper()
	acts := secrets.Actions(providers...)
	if len(acts) != 1 || acts[0].Spec.Name != "secrets.list_refs" {
		t.Fatalf("actions = %+v", acts)
	}
	if err := acts[0].Spec.Validate(); err != nil {
		t.Fatalf("spec is invalid: %v", err)
	}
	return acts[0].Handle(context.Background(), core.ActionRequest{})
}

func TestListRefsShowsReferencesAndNeverAValue(t *testing.T) {
	t.Parallel()
	counted := &resolveCounter{SecretProvider: filledStore(t)}
	res, err := invoke(t, counted)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{secretText, "dmFsdWU", "reveal", "value"} {
		if strings.Contains(strings.ToLower(string(res.Output)), strings.ToLower(leak)) {
			t.Errorf("the result contains %q: %s", leak, res.Output)
		}
	}
	if counted.resolves.Load() != 0 {
		t.Error("listing references resolved a secret")
	}
	var got secrets.RefList
	if err := json.Unmarshal(res.Output, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Refs) != 2 || got.Refs[0] != runKeyRef || got.Refs[1] != webhookRef {
		t.Errorf("refs = %+v, want both references sorted by name", got.Refs)
	}
	var fields map[string][]map[string]any
	if err := json.Unmarshal(res.Output, &fields); err != nil {
		t.Fatal(err)
	}
	for _, ref := range fields["refs"] {
		if len(ref) != 4 {
			t.Errorf("a reference has fields beyond provider, name, class and zone: %v", ref)
		}
	}
}

func TestListRefsRefusesInputAndHidesProviderFailureDetail(t *testing.T) {
	t.Parallel()
	acts := secrets.Actions(filledStore(t))
	_, err := acts[0].Handle(context.Background(), core.ActionRequest{Input: json.RawMessage(`{"name":"x","reveal":true}`)})
	if !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("unknown input = %v, want INVALID_ARGUMENT", err)
	}
	_, err = invoke(t, failingProvider{})
	if !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("a failing provider = %v, want UNAVAILABLE", err)
	}
	if strings.Contains(err.Error(), secretText) {
		t.Errorf("the provider's error text reached the caller: %v", err)
	}
}

func TestListRefsWithNoProviderIsAnEmptyList(t *testing.T) {
	t.Parallel()
	res, err := invoke(t)
	if err != nil || string(res.Output) != `{"refs":[]}` {
		t.Errorf("result = %s, %v", res.Output, err)
	}
}
