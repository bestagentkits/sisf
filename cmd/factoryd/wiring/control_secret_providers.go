package wiring

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/secrets"
	"github.com/bestagentkits/sisf/pkg/secrets/local"
)

// The control zone's secret providers. Secrets named in the approved
// manifest as "env:NAME" or "local:path/name", such as a System One
// credential, resolve through them, and secrets.list_refs lists their
// references. No other zone opens them.
const (
	// EnvLocalStoreDir is the control-owned directory of the encrypted local
	// store and its key, which is made on the first start.
	EnvLocalStoreDir = "SISF_LOCAL_STORE_DIR"
	// EnvControlSecretPrefix marks the variables of the control zone's env
	// file the env provider serves: "env:NAME" is SISF_CONTROL_SECRET_NAME.
	// Nothing else in the environment, least of all the factory's own
	// credentials, is reachable through a reference.
	EnvControlSecretPrefix = "SISF_CONTROL_SECRET_"
	// EnvProviderName is the provider of "env:NAME" references.
	EnvProviderName = "env"
)

// Files of the local store inside EnvLocalStoreDir.
const (
	localStoreFile = "control.secrets"
	localKeyFile   = "control.key"
)

// envSecretName is what an env reference may name: the suffix of a
// variable after EnvControlSecretPrefix.
var envSecretName = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_]*$`)

// ControlSecrets are the control zone's secret providers.
type ControlSecrets struct {
	// Providers list references for secrets.list_refs.
	Providers []secrets.SecretProvider
	// Resolver resolves a reference through the provider it names.
	Resolver secrets.SecretProvider
}

// ControlSecretsFromEnv opens the control zone's secret providers: the env
// provider always, the local store when EnvLocalStoreDir is set. Any zone but
// control is refused, so the agent and application zones gain no provider.
func ControlSecretsFromEnv(zone core.Zone, getenv func(string) string, environ func() []string) (ControlSecrets, error) {
	if zone != core.ZoneControl {
		return ControlSecrets{}, core.NewError(core.CodePermissionDenied, "only the control zone opens the factory's secret providers")
	}
	set := providerSet{EnvProviderName: envProvider{getenv: getenv, environ: environ}}
	if dir := strings.TrimSpace(getenv(EnvLocalStoreDir)); dir != "" {
		store, err := openLocalStore(dir)
		if err != nil {
			return ControlSecrets{}, err
		}
		set[local.ProviderName] = store
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	slices.Sort(names)
	providers := make([]secrets.SecretProvider, 0, len(names))
	for _, name := range names {
		providers = append(providers, set[name])
	}
	return ControlSecrets{Providers: providers, Resolver: set}, nil
}

// openLocalStore opens the encrypted store of the control zone in dir,
// making the directory and the key on the first start.
func openLocalStore(dir string) (*local.Store, error) {
	if !filepath.IsAbs(dir) {
		return nil, core.NewError(core.CodeInvalidArgument, EnvLocalStoreDir+" must be an absolute path")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, core.NewError(core.CodeUnavailable, "the control zone's secrets directory cannot be made").WithCause(err)
	}
	key := filepath.Join(dir, localKeyFile)
	if _, err := os.Lstat(key); os.IsNotExist(err) {
		if err := local.NewKeyFile(key, nil); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, core.NewError(core.CodeUnavailable, "the control zone's secret key cannot be read").WithCause(err)
	}
	return local.Open(local.Config{Path: filepath.Join(dir, localStoreFile), KeyFile: key, Zone: core.ZoneControl})
}

// providerSet resolves a reference through the provider it names.
type providerSet map[string]secrets.SecretProvider

func (s providerSet) Resolve(ctx context.Context, ref secrets.SecretRef) (core.SecretValue, error) {
	p, ok := s[ref.Provider]
	if !ok {
		return core.SecretValue{}, core.NewError(core.CodeNotFound, "no secret provider "+ref.Provider+" is configured in the control zone")
	}
	return p.Resolve(ctx, ref)
}

func (s providerSet) ListRefs(ctx context.Context) ([]secrets.SecretRef, error) {
	var out []secrets.SecretRef
	for _, p := range s {
		refs, err := p.ListRefs(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, refs...)
	}
	return out, nil
}

// envProvider serves the control zone's SISF_CONTROL_SECRET_ variables as
// control-class secrets of the control zone.
type envProvider struct {
	getenv  func(string) string
	environ func() []string
}

func (p envProvider) Resolve(_ context.Context, ref secrets.SecretRef) (core.SecretValue, error) {
	if err := ref.Validate(); err != nil {
		return core.SecretValue{}, err
	}
	switch {
	case ref.Provider != EnvProviderName || !envSecretName.MatchString(ref.Name):
		return core.SecretValue{}, core.NewError(core.CodeInvalidArgument, "secret "+ref.String()+" is not an env reference of upper-case letters, digits and _")
	case ref.Zone != core.ZoneControl || ref.Class != secrets.ClassControl:
		return core.SecretValue{}, core.NewError(core.CodePermissionDenied, "the env provider serves control-class secrets of the control zone only")
	}
	value := p.getenv(EnvControlSecretPrefix + ref.Name)
	if value == "" {
		return core.SecretValue{}, core.NewError(core.CodeNotFound, "secret "+ref.String()+" is not set in the control zone's env file")
	}
	return core.NewSecretValue([]byte(value)), nil
}

// ListRefs lists the variables by name only; values are never read here.
func (p envProvider) ListRefs(context.Context) ([]secrets.SecretRef, error) {
	var out []secrets.SecretRef
	if p.environ == nil {
		return out, nil
	}
	for _, kv := range p.environ() {
		key, value, _ := strings.Cut(kv, "=")
		name, ok := strings.CutPrefix(key, EnvControlSecretPrefix)
		if !ok || value == "" || !envSecretName.MatchString(name) {
			continue
		}
		out = append(out, secrets.SecretRef{Provider: EnvProviderName, Name: name, Class: secrets.ClassControl, Zone: core.ZoneControl})
	}
	slices.SortFunc(out, func(a, b secrets.SecretRef) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}
