package install

import (
	"context"
	"os"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// trustEnvFile is the control area's file that carries the trust volume's
// files into the one-shot trust step of Docker mode. It is filled just
// before the step runs and emptied right after, so the delegated private
// keys rest only on the trust volume.
const trustEnvFile = "trust.env"

// installTrust issues the supervisor's delegated keys for the release `sisf
// up` just signed and places them, with the LKG record of that release, on
// the trust volume: through the one-shot trust step in Docker mode, and
// directly in the data directory in Standalone mode.
func (in *Installer) installTrust(ctx context.Context, m *manifest.Manifest, b built) error {
	files, err := in.trustMaterial(ctx, m.FactoryID, b.release)
	if err != nil {
		return err
	}
	defer func() {
		for _, data := range files {
			clear(data)
		}
	}()
	if m.Mode == manifest.ModeStandalone {
		dir := in.Layout.Data("trust")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return unavailablef(err, "cannot create the trust directory")
		}
		return WriteTrustFiles(dir, files)
	}
	env, err := formatEnv(EnvTrustFiles, encodeTrustFiles(files))
	if err != nil {
		return err
	}
	path := in.Layout.Secret(areaControl, trustEnvFile)
	if err := writeSecret(path, env); err != nil {
		return err
	}
	clear(env)
	in.say("placing the supervisor's delegated keys on its trust volume\n")
	runErr := in.compose(ctx, m, "--profile", "bootstrap", "run", "--rm", "--no-deps", "trust-bootstrap")
	if err := writeSecret(path, nil); err != nil {
		return err
	}
	if runErr != nil {
		return unavailablef(runErr, "the supervisor's trust volume could not be prepared")
	}
	return nil
}
