package install

import (
	"os"
	"path/filepath"
)

// ControlSecretStore is the directory of the control zone's encrypted local
// secret store in Standalone mode: the secrets the Owner stored through the
// factory, such as a System One credential named "local:...", and the key
// that encrypts them. It lies below the data directory because control
// writes it at run time, but it holds secrets, not data a run made.
func (l Layout) ControlSecretStore() string { return l.Data("control", "secrets") }

// removeData removes the data directory of a Standalone install, as
// `sisf down -v` asks, except the control zone's secret store: secrets are
// never removed by down. The store does not depend on the state that goes
// (it has a file and a key of its own), so the next `up` opens it again.
func (in *Installer) removeData() error {
	root := in.Layout.Data()
	keep := in.Layout.ControlSecretStore()
	if _, err := os.Lstat(keep); os.IsNotExist(err) {
		if err := os.RemoveAll(root); err != nil {
			return unavailablef(err, "cannot remove %s", root)
		}
		return nil
	}
	if err := removeAllBut(root, keep); err != nil {
		return unavailablef(err, "cannot remove %s", root)
	}
	return nil
}

// removeAllBut removes everything below dir except keep and the directories
// that lead to it. keep must lie below dir.
func removeAllBut(dir, keep string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		switch rel, err := filepath.Rel(path, keep); {
		case err != nil:
			return err
		case rel == ".":
			continue
		case e.IsDir() && filepath.IsLocal(rel):
			// keep lies below this directory: descend instead of removing.
			if err := removeAllBut(path, keep); err != nil {
				return err
			}
		default:
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		}
	}
	return nil
}
