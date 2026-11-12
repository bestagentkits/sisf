package wiring

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/secrets"
	"github.com/bestagentkits/sisf/pkg/secrets/local"
)

// fakeEnv is a process environment for the providers.
func fakeEnv(vars map[string]string) (func(string) string, func() []string) {
	getenv := func(k string) string { return vars[k] }
	environ := func() []string {
		out := make([]string, 0, len(vars))
		for k, v := range vars {
			out = append(out, k+"="+v)
		}
		return out
	}
	return getenv, environ
}

func controlRef(provider, name string) secrets.SecretRef {
	return secrets.SecretRef{Provider: provider, Name: name, Class: secrets.ClassControl, Zone: core.ZoneControl}
}

func TestOnlyTheControlZoneOpensSecretProviders(t *testing.T) {
	t.Parallel()
	getenv, environ := fakeEnv(map[string]string{EnvControlSecretPrefix + "SYSTEM_ONE_KEY": "s1-value", EnvLocalStoreDir: t.TempDir()})
	for _, zone := range []core.Zone{core.ZoneApplication, core.ZoneAgent} {
		set, err := ControlSecretsFromEnv(zone, getenv, environ)
		if !core.HasCode(err, core.CodePermissionDenied) || set.Resolver != nil || len(set.Providers) != 0 {
			t.Fatalf("zone %s opened %+v (%v), want PERMISSION_DENIED and no provider", zone, set, err)
		}
	}
}

func TestTheEnvProviderServesOnlyTheControlSecretVariables(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	getenv, environ := fakeEnv(map[string]string{
		EnvControlSecretPrefix + "SYSTEM_ONE_KEY": "s1-value",
		EnvControlSecretPrefix + "EMPTY":          "",
		"SISF_GITHUB_TOKEN":                       "factory-github-token",
		"EDGE_COUNTERS_TOKEN":                     "counters-token",
	})
	set, err := ControlSecretsFromEnv(core.ZoneControl, getenv, environ)
	if err != nil {
		t.Fatalf("ControlSecretsFromEnv: %v", err)
	}
	refs, err := set.Resolver.ListRefs(ctx)
	if err != nil || len(refs) != 1 || refs[0] != controlRef(EnvProviderName, "SYSTEM_ONE_KEY") {
		t.Fatalf("ListRefs %+v (%v), want the one set control secret variable", refs, err)
	}
	value, err := set.Resolver.Resolve(ctx, controlRef(EnvProviderName, "SYSTEM_ONE_KEY"))
	if err != nil || string(value.Reveal()) != "s1-value" {
		t.Fatalf("Resolve: %v, want the variable's value", err)
	}
	for _, name := range []string{"SISF_GITHUB_TOKEN", "EDGE_COUNTERS_TOKEN", "EMPTY"} {
		if _, err := set.Resolver.Resolve(ctx, controlRef(EnvProviderName, name)); !core.HasCode(err, core.CodeNotFound) {
			t.Fatalf("env:%s resolved (%v); only %s variables are references", name, err, EnvControlSecretPrefix)
		}
	}
	runClass := secrets.SecretRef{Provider: EnvProviderName, Name: "SYSTEM_ONE_KEY", Class: secrets.ClassRun, Zone: core.ZoneControl}
	agentZone := secrets.SecretRef{Provider: EnvProviderName, Name: "SYSTEM_ONE_KEY", Class: secrets.ClassControl, Zone: core.ZoneAgent}
	for _, ref := range []secrets.SecretRef{runClass, agentZone} {
		if _, err := set.Resolver.Resolve(ctx, ref); !core.HasCode(err, core.CodePermissionDenied) {
			t.Fatalf("Resolve %+v: %v, want PERMISSION_DENIED", ref, err)
		}
	}
	if _, err := set.Resolver.Resolve(ctx, controlRef(EnvProviderName, "lower")); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("a lower-case env reference: %v, want INVALID_ARGUMENT", err)
	}
	if _, err := set.Resolver.Resolve(ctx, controlRef(local.ProviderName, "a")); !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("a local reference without a local store: %v, want NOT_FOUND", err)
	}
}

func TestTheLocalStoreIsMadeOnceAndListedAndResolved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir() + "/secrets"
	getenv, environ := fakeEnv(map[string]string{EnvLocalStoreDir: dir, EnvControlSecretPrefix + "OTHER": "other"})
	first, err := ControlSecretsFromEnv(core.ZoneControl, getenv, environ)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	var writer secrets.Writer
	for _, p := range first.Providers {
		if w, ok := p.(secrets.Writer); ok {
			writer = w
		}
	}
	if writer == nil {
		t.Fatal("no local store among the providers")
	}
	ref := controlRef(local.ProviderName, "decision/system-one")
	if err := writer.Put(ctx, ref, core.NewSecretValue([]byte("s1-local"))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	again, err := ControlSecretsFromEnv(core.ZoneControl, getenv, environ)
	if err != nil {
		t.Fatalf("second open with the key made on the first: %v", err)
	}
	value, err := again.Resolver.Resolve(ctx, ref)
	if err != nil || string(value.Reveal()) != "s1-local" {
		t.Fatalf("Resolve: %v, want the stored value", err)
	}
	out, err := secrets.Actions(again.Providers...)[0].Handle(ctx, core.ActionRequest{})
	if err != nil {
		t.Fatalf("secrets.list_refs: %v", err)
	}
	var list secrets.RefList
	if err := json.Unmarshal(out.Output, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Refs) != 2 || list.Refs[0] != controlRef(EnvProviderName, "OTHER") || list.Refs[1] != ref {
		t.Fatalf("secrets.list_refs %+v, want the env and the local reference", list.Refs)
	}
	if strings.Contains(string(out.Output), "s1-local") || strings.Contains(string(out.Output), `"other"`) {
		t.Fatal("secrets.list_refs carried a value")
	}
}

func TestARelativeSecretsDirIsRefused(t *testing.T) {
	t.Parallel()
	getenv, environ := fakeEnv(map[string]string{EnvLocalStoreDir: "data/secrets"})
	if _, err := ControlSecretsFromEnv(core.ZoneControl, getenv, environ); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("a relative %s: %v, want INVALID_ARGUMENT", EnvLocalStoreDir, err)
	}
}
