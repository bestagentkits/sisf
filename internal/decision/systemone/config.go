// Package systemone is the typed HTTP JSON adapter for the System One
// decision models, Jev and Clef.
//
// Each backend is reached at one endpoint and pinned to one protocol
// version. The adapter asks the typed questions it is given and accepts only
// a typed judgment that answers exactly those questions under that version.
// An error, a timeout, another version, an unknown field or a judgment that
// breaks the schema fails closed: the caller gets an error and creates no
// work. The adapter grants nothing; what a judgment may change is decided by
// the caller, where deterministic checks keep their veto.
package systemone

import (
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/secrets"
)

// Backend names a System One model service.
type Backend string

const (
	BackendJev  Backend = "jev"
	BackendClef Backend = "clef"
)

// Valid reports whether b is a known backend.
func (b Backend) Valid() bool { return b == BackendJev || b == BackendClef }

// MaxTimeout bounds how long one judgment may take.
const MaxTimeout = 2 * time.Minute

// versionPattern accepts an explicit version such as "1", "v2" or
// "2026-11-01", never a floating name such as "latest".
var versionPattern = regexp.MustCompile(`^v?[0-9][A-Za-z0-9._-]{0,63}$`)

// Config is one System One backend as the factory configuration declares
// it. The credential is a secret reference, never a value.
type Config struct {
	Backend  Backend `json:"backend" yaml:"backend"`
	Endpoint string  `json:"endpoint" yaml:"endpoint"`
	// Version is the protocol version the backend must answer with.
	Version string        `json:"version" yaml:"version"`
	Timeout time.Duration `json:"timeout" yaml:"timeout"`
	// Credential is a reference written "env:NAME" or "local:path/name";
	// empty means the backend takes no credential.
	Credential string `json:"credential,omitempty" yaml:"credential,omitempty"`
	// Model is the route a judgment must report; any other is refused.
	Model core.ModelRoute `json:"model" yaml:"model"`
}

// Validate rejects a configuration that is incomplete, unpinned or carries
// a credential inline. Messages never repeat the refused value.
func (c Config) Validate() error {
	switch {
	case !c.Backend.Valid():
		return invalid("system one backend must be jev or clef")
	case !versionPattern.MatchString(c.Version):
		return invalid("system one needs an explicit protocol version such as 1 or 2026-11-01")
	case c.Timeout <= 0 || c.Timeout > MaxTimeout:
		return invalid("system one needs a timeout above zero and at most " + MaxTimeout.String())
	case c.Credential != "" && !manifest.IsSecretRef(c.Credential):
		return invalid("the system one credential must be " + manifest.SecretRefForm + ", never a value")
	}
	if err := c.Model.Validate(); err != nil {
		return invalid("system one needs the model route its judgments report")
	}
	return validateEndpoint(c.Endpoint)
}

// validateEndpoint requires an absolute URL with no user information, query
// or fragment, so a credential cannot hide in it. Plain HTTP is accepted
// only on a loopback address.
func validateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return invalid("the system one endpoint must be an absolute URL")
	}
	switch {
	case u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery:
		return invalid("the system one endpoint must not carry user information, a query or a fragment")
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && loopback(u.Hostname()):
		return nil
	}
	return invalid("the system one endpoint must use HTTPS, or HTTP on a loopback address")
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// CredentialRef returns the secret reference of the credential and whether
// one is configured. The secret is a control-plane credential of Zone 3.
func (c Config) CredentialRef() (secrets.SecretRef, bool, error) {
	if c.Credential == "" {
		return secrets.SecretRef{}, false, nil
	}
	if !manifest.IsSecretRef(c.Credential) {
		return secrets.SecretRef{}, false, invalid("the system one credential must be " + manifest.SecretRefForm)
	}
	provider, name, _ := strings.Cut(c.Credential, ":")
	ref := secrets.SecretRef{Provider: provider, Name: name, Class: secrets.ClassControl, Zone: core.ZoneControl}
	return ref, true, ref.Validate()
}

func invalid(message string) error { return core.NewError(core.CodeInvalidArgument, message) }
