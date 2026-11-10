package install

import (
	"path/filepath"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Areas of the secrets directory. Each zone has an area of its own, and no
// unit is ever given the files of an area that is not its zone's. The state
// area belongs to the state store alone, sim to the fake GitHub, and edge to
// the edge, which holds the admin token of its traffic split.
const (
	areaApplication = "application"
	areaAgent       = "agent"
	areaControl     = "control"
	areaState       = "state"
	areaSim         = "sim"
	areaEdge        = "edge"
)

// zoneArea returns the secrets area of a zone.
func zoneArea(z core.Zone) string {
	switch z {
	case core.ZoneApplication:
		return areaApplication
	case core.ZoneAgent:
		return areaAgent
	default:
		return areaControl
	}
}

// Layout is where one install keeps its files. Everything lives below Dir,
// so removing the directory removes the install, and nothing outside it is
// touched by `sisf init`, `up` or `down`.
type Layout struct{ Dir string }

// Config is the factory manifest: desired state, with secret references only.
func (l Layout) Config() string { return filepath.Join(l.Dir, "factory.yaml") }

// ReleaseManifest is the signed release manifest the supervisor verifies.
func (l Layout) ReleaseManifest() string {
	return filepath.Join(l.Dir, "release", "release-manifest.json")
}

// Compose is the rendered Compose file of Docker mode.
func (l Layout) Compose() string { return filepath.Join(l.Dir, "compose.yaml") }

// BudgetConfirmation records the Owner's install-time budget confirmation.
func (l Layout) BudgetConfirmation() string {
	return filepath.Join(l.Dir, "budget-confirmation.json")
}

// SimAgentDir holds the scripted agent's patch of the simulation profile.
// It is not secret: the dispatcher reads it, mounted read-only in Docker mode.
func (l Layout) SimAgentDir() string { return filepath.Join(l.Dir, "sim", "agent") }

// SecretsDir is the directory of one secrets area.
func (l Layout) SecretsDir(area string) string { return filepath.Join(l.Dir, "secrets", area) }

// Secret is one file of a secrets area.
func (l Layout) Secret(area, name string) string { return filepath.Join(l.SecretsDir(area), name) }

// RuntimeEnv is the environment file of the units of one area.
func (l Layout) RuntimeEnv(area string) string { return l.Secret(area, "runtime.env") }

// Bin is a built program of Standalone mode.
func (l Layout) Bin(name string) string { return filepath.Join(l.Dir, "bin", name) }

// Data is a directory below the Standalone data root.
func (l Layout) Data(parts ...string) string {
	return filepath.Join(append([]string{l.Dir, "data"}, parts...)...)
}

// StateDB is the SQLite database of Standalone mode.
func (l Layout) StateDB() string { return l.Data("state", "factory.db") }

// Logs is the directory of Standalone process logs.
func (l Layout) Logs() string { return filepath.Join(l.Dir, "logs") }

// PIDFile names the foreground `sisf up` process of Standalone mode.
func (l Layout) PIDFile() string { return filepath.Join(l.Dir, "run", "sisf.pid") }

// ServiceDir holds the launchd and systemd units, ready to copy.
func (l Layout) ServiceDir() string { return filepath.Join(l.Dir, "service") }

// ResolveLocal maps a "local:path/name" secret reference to its file below
// the secrets directory. Any other form is refused; the manifest pattern
// already forbids dot segments, and this check holds even if it changes.
func (l Layout) ResolveLocal(ref string) (string, error) {
	rest, ok := strings.CutPrefix(ref, "local:")
	if !ok || !manifest.IsSecretRef(ref) {
		return "", invalidf("secret reference must be %s", manifest.SecretRefForm)
	}
	root := filepath.Join(l.Dir, "secrets")
	full := filepath.Join(root, filepath.FromSlash(rest))
	if rel, err := filepath.Rel(root, full); err != nil || strings.HasPrefix(rel, "..") {
		return "", invalidf("secret reference leaves the secrets directory")
	}
	return full, nil
}
