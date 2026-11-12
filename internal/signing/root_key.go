package signing

import (
	"crypto/ed25519"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// ResolvePublicKey loads the root public key that a manifest names in
// supervisor.root_key_ref. The reference has the form the manifest contract
// defines: "env:NAME", an environment variable, or "local:path/name", an
// entry of the factory's encrypted store.
//
// Only env: can be resolved here. The encrypted store is not part of this
// package, so a local: reference returns NOT_SUPPORTED instead of a guess;
// its owner resolves the entry and hands the text to ParsePublicKey.
//
// lookupEnv reads the environment; pass os.LookupEnv outside tests.
//
// An empty reference, or one that leads to nothing, returns NOT_FOUND. A
// value that is not a reference, or a reference that leads to something
// other than a public key, returns INVALID_ARGUMENT; the value is not
// repeated in the error, in case it is a key written inline. There is no
// default key: without the root key nothing can be verified, and the caller
// must refuse to go on.
func ResolvePublicKey(ref string, lookupEnv func(string) (string, bool)) (ed25519.PublicKey, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, missingKey("no root public key is configured")
	}
	if !manifest.IsSecretRef(ref) {
		return nil, invalid("the root key reference must be %s", manifest.SecretRefForm)
	}
	source, name, _ := strings.Cut(ref, ":")
	if source != "env" {
		return nil, core.NewError(core.CodeNotSupported,
			sprintf("a root public key in the %s: store cannot be read here; provide it as env:NAME", source))
	}
	value, ok := lookupEnv(name)
	if !ok || strings.TrimSpace(value) == "" {
		return nil, missingKey("environment variable %s holds no root public key", name)
	}
	return ParsePublicKey(value)
}

// missingKey builds the NOT_FOUND error for an absent root key.
func missingKey(format string, args ...any) *core.Error {
	return core.NewError(core.CodeNotFound, sprintf(format, args...))
}
