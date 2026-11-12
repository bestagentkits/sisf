package install

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// PriorState is what a lifecycle command finds before it acts, so that it
// can say what it did rather than what it would have done.
type PriorState struct {
	// Running says the factory of this install runs.
	Running bool
	// NeverStarted says this install never made its Docker Compose project,
	// so it has no container, network or volume to stop or remove.
	NeverStarted bool
	// ForeignData names, for an install that never started, whose volumes
	// carry its Compose project's name: "the install in <dir>", or an
	// install this directory does not know. Empty when there are none.
	ForeignData string
}

// PriorState reports whether the factory runs and, in Docker mode, whether
// this install ever started its Compose project and whose data has that
// project's name.
func (in *Installer) PriorState(ctx context.Context) (PriorState, error) {
	m, err := in.Layout.LoadManifest()
	if err != nil {
		return PriorState{}, err
	}
	if m.Mode != manifest.ModeDocker {
		running, err := in.Running()
		return PriorState{Running: running}, err
	}
	if !in.composeRendered() {
		return PriorState{NeverStarted: true, ForeignData: in.foreignData(ctx, m)}, nil
	}
	running, _ := in.instanceRunning(ctx, m) // a Compose that does not answer runs nothing of this install
	return PriorState{Running: running}, nil
}

// foreignData names the install whose volumes carry the Compose project
// name of this install, which never started its project. Nothing of this
// install is removed or stopped either way; a Docker that does not answer
// only leaves the other install unnamed, so its error is not the command's.
func (in *Installer) foreignData(ctx context.Context, m *manifest.Manifest) string {
	volumes, err := in.projectVolumes(ctx, m)
	if err != nil {
		return ""
	}
	for _, v := range volumes {
		if v.owner != "" && !sameFile(v.owner, in.Layout.Dir) {
			return "the install in " + v.owner
		}
	}
	if len(volumes) > 0 {
		return "an install this directory does not know"
	}
	return ""
}
