package install

import (
	"os"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// A Docker install that never started its Compose project is not running
// and has no data of its own; volumes of its project's name that another
// install made are named with that install's directory.
func TestTheStateBeforeALifecycleCommandSaysWhatADockerInstallHas(t *testing.T) {
	t.Parallel()
	answerVolumes := func(exec *fakeExec, listed string, ps string) {
		exec.answer = func(c Cmd) ([]byte, error) {
			switch {
			case len(c.Args) > 1 && strings.Join(c.Args[:2], " ") == "volume ls":
				return []byte(listed), nil
			case strings.Contains(strings.Join(c.Args, " "), " ps --format json"):
				return []byte(ps), nil
			}
			return buildAnswers(c)
		}
	}
	in, exec, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeDocker)
	answerVolumes(exec, "sisf-hello-app-state-data /elsewhere/other\n", "")
	prior, err := in.PriorState(t.Context())
	if err != nil || prior.Running || !prior.NeverStarted || prior.ForeignData != "the install in /elsewhere/other" {
		t.Fatalf("never started next to another install's volumes = %+v %v", prior, err)
	}
	answerVolumes(exec, "sisf-hello-app-state-data \n", "")
	if prior, err := in.PriorState(t.Context()); err != nil || prior.ForeignData != "an install this directory does not know" {
		t.Fatalf("never started next to unnamed volumes = %+v %v", prior, err)
	}
	answerVolumes(exec, "", "")
	if prior, err := in.PriorState(t.Context()); err != nil || !prior.NeverStarted || prior.ForeignData != "" || prior.Running {
		t.Fatalf("never started, nothing there = %+v %v", prior, err)
	}

	// Once rendered, Compose says whether the project runs.
	if err := os.WriteFile(in.Layout.Compose(), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	answerVolumes(exec, "", "")
	if prior, err := in.PriorState(t.Context()); err != nil || prior.NeverStarted || prior.Running {
		t.Fatalf("rendered and stopped = %+v %v", prior, err)
	}
	answerVolumes(exec, "", `{"Service":"control","State":"running"}`)
	if prior, err := in.PriorState(t.Context()); err != nil || prior.NeverStarted || !prior.Running {
		t.Fatalf("rendered and running = %+v %v", prior, err)
	}
}
