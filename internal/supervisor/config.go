// Package supervisor is the boundary of the trusted release supervisor, the
// one unit that holds deploy rights and signing keys.
//
// The supervisor belongs to the factory deployment yet stays outside the
// area a release candidate can modify. This package enforces the part of
// that boundary a running process can enforce by itself:
//
//   - at startup it measures its own executable and refuses to run unless
//     the digest equals the one pinned in the release manifest signed with
//     the Owner's root key;
//   - it keeps the last known good (LKG) record only on the trust volume,
//     the directory no other zone can write, and accepts no other place;
//   - it answers status questions on a local socket that cannot change
//     anything.
//
// It performs no release steps. Staging, promotion and rollback are added on
// top of this boundary.
package supervisor

import (
	"fmt"
	"path/filepath"

	"github.com/bestagentkits/sisf/pkg/core"
)

const (
	// DefaultAdminAddr is the admin address used when none is configured:
	// the supervisor's port on the loopback interface.
	DefaultAdminAddr = "127.0.0.1:7464"
	// DefaultLKGFile is the name of the LKG record inside the trust volume.
	DefaultLKGFile = "lkg.json"
)

// Config is what an installation tells its supervisor. It holds locations
// and references only, never key material.
type Config struct {
	// FactoryID is the deployment this supervisor belongs to. A release
	// manifest signed for any other factory is refused.
	FactoryID core.FactoryID
	// ManifestPath locates the signed release manifest.
	ManifestPath string
	// RootKeyRef locates the root public key the manifest is verified with.
	// It is supervisor.root_key_ref of factory.yaml, for example
	// env:SISF_ROOT_PUBLIC_KEY.
	RootKeyRef string
	// TrustDir is where the trust volume is mounted: the directory only the
	// supervisor writes, holding the LKG record.
	TrustDir string
	// LKGPath is the LKG record. It must lie inside TrustDir and defaults to
	// DefaultLKGFile there.
	LKGPath string
	// AdminAddr is the status socket: "unix:/absolute/path" or a loopback
	// "host:port". It defaults to DefaultAdminAddr.
	AdminAddr string
}

// withDefaults validates what can be judged without touching the system and
// fills in the optional settings. A missing root key reference is left for
// the key lookup to report, so the refusal says what is missing.
func (c Config) withDefaults() (Config, error) {
	if err := c.FactoryID.Validate(); err != nil {
		return Config{}, err
	}
	if c.ManifestPath == "" {
		return Config{}, invalid("the supervisor needs the path of the signed release manifest")
	}
	if c.TrustDir == "" {
		return Config{}, invalid("the supervisor needs the directory of its trust volume")
	}
	if c.LKGPath == "" {
		c.LKGPath = filepath.Join(c.TrustDir, DefaultLKGFile)
	}
	if c.AdminAddr == "" {
		c.AdminAddr = DefaultAdminAddr
	}
	return c, nil
}

// newError builds a typed error with a formatted message.
func newError(code core.ErrorCode, format string, args ...any) *core.Error {
	return core.NewError(code, fmt.Sprintf(format, args...))
}

// invalid builds the INVALID_ARGUMENT error used by this package.
func invalid(format string, args ...any) *core.Error {
	return newError(core.CodeInvalidArgument, format, args...)
}
