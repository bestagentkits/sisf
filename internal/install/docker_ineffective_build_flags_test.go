package install

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Docker mode builds and releases nothing, whatever the build command and
// the QA agent's launcher are: the supervisor cannot observe an image a
// sandboxed build reports. A flag that would change nothing is refused by
// init and by the up that creates the install, never taken in silence.
func TestADockerInstallRefusesTheBuildFlagsItCannotUse(t *testing.T) {
	t.Parallel()
	for name, set := range map[string]func(o *Options){
		"--build-command":   func(o *Options) { o.BuildCommand = "/opt/sisf/build-candidate" },
		"--qa-agent-binary": func(o *Options) { o.QAAgentBinary = "/opt/sisf/qa-launcher" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, profile := range []func(o *Options){production, func(*Options) {}} {
				in, _, _ := newTestInstaller(t)
				o := initOptions(in, manifest.ModeDocker)
				profile(&o)
				set(&o)
				if _, err := in.Init(t.Context(), o); !core.HasCode(err, core.CodeInvalidArgument) {
					t.Fatalf("init of a Docker install with %s: %v, want INVALID_ARGUMENT", name, err)
				}
				if err := in.Up(t.Context(), UpOptions{Init: o}); !core.HasCode(err, core.CodeInvalidArgument) {
					t.Fatalf("up of a new Docker install with %s: %v, want INVALID_ARGUMENT", name, err)
				}
				if done, _ := in.Layout.installed(); done {
					t.Fatalf("a Docker install was created with %s", name)
				}
			}
		})
	}
}

// An up of an existing Docker install refuses them too, rather than start
// the install as if they had been taken.
func TestAnUpOfADockerInstallRefusesTheBuildFlagsItCannotUse(t *testing.T) {
	t.Parallel()
	in, exec := upDocker(t)
	before := len(exec.lines())
	o := initOptions(in, manifest.ModeDocker)
	o.BuildCommand = "/opt/sisf/build-candidate"
	if err := in.Up(t.Context(), UpOptions{Init: o}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("up of an existing Docker install with --build-command: %v, want INVALID_ARGUMENT", err)
	}
	if ran := exec.lines()[before:]; len(ran) != 0 {
		t.Fatalf("the refused up ran %v", ran)
	}
}
