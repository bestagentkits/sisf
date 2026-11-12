package install

import (
	"context"
	"os"
	"path/filepath"

	"github.com/bestagentkits/sisf/internal/conformance"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// release builds the artifacts of the install, pins them in the manifest,
// and writes a release manifest signed with the install's root key. The
// supervisor verifies that file at startup, so a unit that is not the one
// signed here never gets the supervisor's trust.
func (in *Installer) release(ctx context.Context, m *manifest.Manifest) (built, error) {
	var b built
	var err error
	if m.Mode == manifest.ModeDocker {
		b, err = in.buildDocker(ctx, project(m.FactoryID))
	} else {
		b, err = in.buildStandalone(ctx)
	}
	if err != nil {
		return built{}, err
	}
	applyPins(m, b)
	if err := conformance.Check(m).Err(); err != nil {
		return built{}, err
	}
	if err := in.signRelease(ctx, m, b); err != nil {
		return built{}, err
	}
	return b, writeManifest(in.Layout.Config(), m)
}

// applyPins records the digests of the built units in the manifest. Units
// that are not built yet, such as the runners, keep their placeholder.
func applyPins(m *manifest.Manifest, b built) {
	for name, digest := range b.units {
		if u, ok := m.Units[name]; ok && name != manifest.UnitState {
			u.Digest = digest
			m.Units[name] = u
		}
	}
	if m.Supervisor != nil {
		m.Supervisor.Digest = b.units[manifest.UnitSupervisor]
	}
}

// signRelease signs the release manifest with the root key and writes it.
func (in *Installer) signRelease(ctx context.Context, m *manifest.Manifest, b built) error {
	pem, err := os.ReadFile(in.Layout.Secret(areaControl, "root-key.pem"))
	if err != nil {
		return unavailablef(err, "the root key cannot be read")
	}
	priv, err := signing.ParsePrivateKey(pem)
	if err != nil {
		return err
	}
	signer, err := signing.NewSigner(priv)
	if err != nil {
		return err
	}
	pins := map[manifest.UnitName]core.Digest{}
	for name := range b.units {
		pins[name] = m.Units[name].Digest
	}
	rm := signing.ReleaseManifest{
		APIVersion: signing.ReleaseAPIVersion, FactoryID: m.FactoryID, IssuedAt: in.Clock.Now().UTC(),
		Units: pins, SupervisorExecutable: b.supervisorExe,
	}
	signed, err := signing.SignManifest(ctx, signer, rm)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(in.Layout.ReleaseManifest()), 0o755); err != nil {
		return unavailablef(err, "cannot create the release directory")
	}
	return signing.WriteSignedManifest(in.Layout.ReleaseManifest(), signed)
}
