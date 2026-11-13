package install

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// noSandboxStandalone creates a production Standalone install without a
// Docker build sandbox, as `sisf init` does with the default --exec.
func noSandboxStandalone(t *testing.T) (*Installer, *fakeExec, InitResult) {
	t.Helper()
	in, exec, _ := newTestInstaller(t)
	o := initOptions(in, manifest.ModeStandalone)
	production(&o)
	res, err := in.Init(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	return in, exec, res
}

// Outside the simulation the supervisor builds candidates only in a Docker
// build sandbox and refuses the local execution backend, which cannot
// isolate untrusted code; in Standalone mode it is also what runs the
// application. An install without a sandbox therefore cannot start: init
// says so and names the sandbox flags, and up refuses once, before it
// builds or starts anything, rather than restart a supervisor that refuses
// forever.
func TestAStandaloneInstallWithoutABuildSandboxIsRefusedOnceAtUp(t *testing.T) {
	t.Parallel()
	in, exec, res := noSandboxStandalone(t)
	if !slices.ContainsFunc(res.Warnings, func(w string) bool {
		return strings.Contains(w, "--exec docker") && strings.Contains(w, "--runner-image") && strings.Contains(w, "sisf up")
	}) {
		t.Errorf("init did not say up will refuse the install and name the build sandbox flags: %q", res.Warnings)
	}
	if strings.Contains(res.Next, "sisf up") || !strings.Contains(res.Next, "sisf init") {
		t.Errorf("init names %q as the next step of an install that cannot start", res.Next)
	}
	// No service unit is written that would only restart a refused up, and
	// none can be written later.
	if entries, _ := os.ReadDir(in.Layout.ServiceDir()); len(entries) != 0 {
		t.Errorf("init wrote service units for an install that cannot start: %v", entries)
	}
	if err := in.WriteServiceUnits(); !Refused(err) || !strings.Contains(err.Error(), "--exec docker") {
		t.Errorf("service units of an install that cannot start: %v, want the refusal naming the sandbox flags", err)
	}
	before := len(exec.lines())
	err := in.Up(t.Context(), UpOptions{})
	if !core.HasCode(err, core.CodeInvalidArgument) || !Refused(err) || !strings.Contains(err.Error(), "--exec docker") {
		t.Fatalf("up of a Standalone install without a build sandbox: %v, want a refusal naming the sandbox flags", err)
	}
	if ran := exec.lines()[before:]; len(ran) != 0 {
		t.Fatalf("the refused up ran %v", ran)
	}
	m, err := in.Layout.LoadManifest()
	must(t, err)
	var c collector
	in.checkStages(&c, m)
	if v := verdictOf(c.report, "build sandbox"); len(v) != 1 || v[0] != VerdictFail || !strings.Contains(detailOf(c.report, "build sandbox"), "--exec docker") {
		t.Fatalf("build sandbox = %v %q", v, detailOf(c.report, "build sandbox"))
	}
}

// An up of an empty directory does not create an install that could never
// start: it refuses before it writes anything.
func TestAnUpThatWouldCreateAStandaloneInstallWithoutABuildSandboxCreatesNothing(t *testing.T) {
	t.Parallel()
	in, exec, _ := newTestInstaller(t)
	o := initOptions(in, manifest.ModeStandalone)
	production(&o)
	err := in.Up(t.Context(), UpOptions{Init: o})
	if !Refused(err) || !strings.Contains(err.Error(), "--exec docker") {
		t.Fatalf("up = %v, want the refusal naming the sandbox flags", err)
	}
	if entries, _ := os.ReadDir(in.Layout.Dir); len(entries) != 0 {
		t.Fatalf("the refused up wrote %v", entries)
	}
	if ran := exec.lines(); len(ran) != 0 {
		t.Fatalf("the refused up ran %v", ran)
	}
}

// The simulation builds nothing an agent wrote, so it needs no sandbox.
func TestASimulatedStandaloneInstallNeedsNoBuildSandbox(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	res, err := in.Init(t.Context(), initOptions(in, manifest.ModeStandalone))
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "--exec docker") }) {
		t.Errorf("init asks a simulation for a build sandbox: %q", res.Warnings)
	}
	if res.Next != "sisf up --dir "+in.Layout.Dir {
		t.Errorf("next step = %q", res.Next)
	}
	m, err := in.Layout.LoadManifest()
	must(t, err)
	if err := in.standaloneRunnable(m); err != nil {
		t.Fatalf("a simulated install is refused: %v", err)
	}
	var c collector
	in.checkStages(&c, m)
	if v := verdictOf(c.report, "build sandbox"); len(v) != 0 {
		t.Fatalf("build sandbox = %v %q", v, detailOf(c.report, "build sandbox"))
	}
}
