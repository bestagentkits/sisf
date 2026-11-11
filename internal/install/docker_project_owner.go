package install

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// composeProject is the part of `docker compose ls --format json` read here.
type composeProject struct {
	Name        string `json:"Name"`
	ConfigFiles string `json:"ConfigFiles"`
}

// checkProjectOwner refuses to act on a Compose project of this install's
// name that another install made. The project is named after the factory
// id, which two installs of same-named repositories share by default; up,
// stop or down -v in one directory would otherwise change or delete the
// other's containers and state volume.
func (in *Installer) checkProjectOwner(ctx context.Context, m *manifest.Manifest) error {
	name := project(m.FactoryID)
	out, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: []string{"compose", "ls", "--all", "--format", "json", "--filter", "name=" + name}})
	if err != nil {
		return unavailablef(err, "docker compose cannot list its projects; is Docker running?")
	}
	var projects []composeProject
	if strings.TrimSpace(string(out)) != "" {
		if err := json.Unmarshal(out, &projects); err != nil {
			return unavailablef(err, "docker compose listed its projects in a form this sisf cannot read")
		}
	}
	for _, p := range projects {
		if p.Name != name || p.ConfigFiles == "" {
			continue
		}
		if !slices.ContainsFunc(strings.Split(p.ConfigFiles, ","), in.isOwnComposeFile) {
			return core.NewError(core.CodeConflict, "the Compose project "+name+" belongs to another install (its Compose file is "+p.ConfigFiles+
				"); nothing was changed. Two installs of factory "+string(m.FactoryID)+" cannot run in Docker on one host",
				core.NextAction{Action: "sisf init --factory-id ID", Description: "make this install again under a factory id of its own"})
		}
	}
	return nil
}

// isOwnComposeFile reports whether path, as Compose recorded it, is this
// install's Compose file: the same path, or the same file reached through a
// symbolic link, such as /var and /private/var on macOS.
func (in *Installer) isOwnComposeFile(path string) bool {
	own := in.Layout.Compose()
	if path == own {
		return true
	}
	a, errA := os.Stat(path)
	b, errB := os.Stat(own)
	return errA == nil && errB == nil && os.SameFile(a, b)
}

// composeRendered reports whether `sisf up` ever rendered the project of
// this install. Without the file nothing of the project was started.
func (in *Installer) composeRendered() bool { return fileExists(in.Layout.Compose()) }
