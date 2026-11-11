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

// installDirLabel is the label of every volume of an install's Compose
// project that names the directory of the install that made it.
const installDirLabel = "sisf.install-dir"

// checkProjectOwner refuses to act on a Compose project of this install's
// name that another install made. The project is named after the factory
// id, which two installs of same-named repositories share by default; up,
// stop or down -v in one directory would otherwise change or delete the
// other's containers and state volume. Containers are checked through
// Compose's list of projects, and volumes, which outlive the containers
// after a `sisf down` without -v, through their label.
func (in *Installer) checkProjectOwner(ctx context.Context, m *manifest.Manifest) error {
	if err := in.checkProjectContainers(ctx, m); err != nil {
		return err
	}
	return in.checkProjectVolumes(ctx, m)
}

// checkProjectContainers refuses a project that has containers and was
// started from another install's Compose file.
func (in *Installer) checkProjectContainers(ctx context.Context, m *manifest.Manifest) error {
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

// checkProjectVolumes refuses a project whose volumes another install
// made: a volume labelled with another install's directory, or one with no
// label while this install never rendered its project, as volumes made
// before the label existed are. A project this install rendered keeps its
// own unlabelled volumes: Compose does not relabel a volume that exists.
func (in *Installer) checkProjectVolumes(ctx context.Context, m *manifest.Manifest) error {
	name := project(m.FactoryID)
	// Each line is the volume's name, which has no space, then the label.
	out, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: []string{"volume", "ls",
		"--filter", "label=com.docker.compose.project=" + name,
		"--format", `{{.Name}} {{.Label "` + installDirLabel + `"}}`}})
	if err != nil {
		return unavailablef(err, "docker cannot list the volumes of project %s; is Docker running?", name)
	}
	rendered := in.composeRendered()
	for _, line := range strings.Split(string(out), "\n") {
		volume, owner, _ := strings.Cut(strings.TrimRight(line, "\r"), " ")
		owner = strings.TrimSpace(owner)
		switch {
		case volume == "":
			continue
		case owner != "" && !sameFile(owner, in.Layout.Dir):
			return core.NewError(core.CodeConflict, "volume "+volume+" of the Compose project "+name+" belongs to the install in "+owner+
				"; nothing was changed. Two installs of factory "+string(m.FactoryID)+" cannot run in Docker on one host",
				core.NextAction{Action: "sisf down -v --dir " + owner, Description: "remove the other install's data, if you no longer need it"},
				core.NextAction{Action: "sisf init --factory-id ID", Description: "make this install again under a factory id of its own"})
		case owner == "" && !rendered:
			return core.NewError(core.CodeConflict, "volume "+volume+" of the Compose project "+name+
				" was left by an install this directory does not know; nothing was changed, so its data is not adopted. Two installs of factory "+string(m.FactoryID)+" cannot run in Docker on one host",
				core.NextAction{Action: "docker volume ls --filter label=com.docker.compose.project=" + name, Description: "see the volumes; remove them only if no install needs their data"},
				core.NextAction{Action: "sisf init --factory-id ID", Description: "make this install again under a factory id of its own"})
		}
	}
	return nil
}

// isOwnComposeFile reports whether path, as Compose recorded it, is this
// install's Compose file.
func (in *Installer) isOwnComposeFile(path string) bool { return sameFile(path, in.Layout.Compose()) }

// sameFile reports whether two paths name one file: the same path, or the
// same file reached through a symbolic link, such as /var and /private/var
// on macOS.
func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	sa, errA := os.Stat(a)
	sb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(sa, sb)
}

// composeRendered reports whether `sisf up` ever rendered the project of
// this install. Without the file nothing of the project was started.
func (in *Installer) composeRendered() bool { return fileExists(in.Layout.Compose()) }
