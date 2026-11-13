package install

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

func TestStandaloneRunsTheLifecycleWithNativeProcessesAndTheLocalBackends(t *testing.T) {
	t.Parallel()
	r := startStandalone(t, nil)
	r.stop(t)
	sup := r.proc(t, "sisf-supervisor").cmd.Args
	for _, want := range []string{
		"--exec=local", "--deploy=local", "--qa-judge=fake", "--coder-agent=fake", "--release-window=10s",
		"--edge-admin-url=http://127.0.0.1:7469", "--dispatcher-admin-url=http://127.0.0.1:7466",
		"--deploy-local-program=" + r.in.Layout.Bin(appLauncher), "--deploy-local-installed-program=" + r.in.Layout.Bin("hello-app"), "--factory-manifest=" + r.in.Layout.Config(),
	} {
		if !slices.Contains(sup, want) {
			t.Errorf("the supervisor lacks %s: %v", want, sup)
		}
	}
	edge := r.proc(t, "sisf-edge").cmd.Args
	for _, want := range []string{"--admin-listen=127.0.0.1:7469", "--egress-listen=", "--route=/report=http://127.0.0.1:7463"} {
		if !slices.Contains(edge, want) {
			t.Errorf("the edge lacks %s: %v", want, edge)
		}
	}
	dispatcher := r.factoryd(t, "dispatcher").cmd.Args
	if !slices.Contains(dispatcher, "--admin-listen=127.0.0.1:7466") || !slices.Contains(dispatcher, "--fake-agent-work=20s") {
		t.Errorf("the dispatcher = %v", dispatcher)
	}
	// Nothing starts the application but the supervisor.
	for _, p := range r.procs {
		if strings.HasSuffix(p.cmd.Name, "/hello-app") || strings.HasSuffix(p.cmd.Name, "/"+appLauncher) {
			t.Errorf("%s is started as a unit", p.cmd.Name)
		}
	}
	script, err := os.ReadFile(r.in.Layout.Bin(appLauncher))
	must(t, err)
	info, _ := os.Stat(r.in.Layout.Bin(appLauncher))
	if info.Mode().Perm() != 0o755 || !strings.Contains(string(script), `HELLO_ADDR="$SISF_LISTEN"`) ||
		!strings.Contains(string(script), "program='"+r.in.Layout.Bin("hello-app")+"'") || !strings.Contains(string(script), `exec "$program"`) {
		t.Errorf("the launcher (%04o):\n%s", info.Mode().Perm(), script)
	}
	if _, err := readDelegations(dirReader(r.in.Layout.Data("trust")), "sisf-hello-app"); err != nil {
		t.Errorf("the delegated keys are not on the trust directory: %v", err)
	}
	if _, err := os.Stat(r.in.Layout.SimAgentDir() + "/" + simPatchFile); err != nil {
		t.Errorf("the scripted agent has no patch: %v", err)
	}
}

func TestAProductionStandaloneInstallRunsTheAgentJudge(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	c := lifecycleChoice(Options{QAAgent: "claude"}, manifest.ModeStandalone)
	args := strings.Join(in.standaloneSupervisorArgs(&manifest.Manifest{FactoryID: "sisf-shop"}, c), " ")
	for _, want := range []string{"--qa-judge=agent", "--qa-agent=claude", "--exec=local"} {
		if !strings.Contains(args, want) {
			t.Errorf("the supervisor lacks %s: %s", want, args)
		}
	}
	if strings.Contains(args, "--release-window") {
		t.Error("a production install runs the simulation's release window")
	}
	if err := (LifecycleChoice{QAAgent: "gpt"}).check(); err == nil {
		t.Error("an unknown QA agent was accepted")
	}
	if err := (LifecycleChoice{QAAgentBinary: "relative/launcher"}).check(); err == nil {
		t.Error("a relative launcher was accepted")
	}
	for _, bad := range []string{"make; curl x", "make  build", " make", `make "x"`, "make $HOME", "make\nx", "a: b", "make #x"} {
		if err := (LifecycleChoice{BuildCommand: bad}).check(); err == nil {
			t.Errorf("the build command %q was accepted", bad)
		}
	}
	if err := (LifecycleChoice{BuildCommand: "/opt/sisf/build-candidate --unit=app"}).check(); err != nil {
		t.Errorf("a plain build command was refused: %v", err)
	}
}

// Outside the simulation the scripted coding agent, the default when no
// real one is chosen, leaves the install's own patch, which a test or the
// Owner may replace, after the same short work as in the simulation.
func TestTheScriptedCoderOfAProductionInstallLeavesTheInstallsPatch(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	args := in.standaloneDispatcherArgs(lifecycleChoice(Options{}, manifest.ModeStandalone), false)
	for _, want := range []string{"--fake-agent-patch=" + in.Layout.SimAgentDir() + "/" + simPatchFile, "--fake-agent-work=" + simAgentWork} {
		if !slices.Contains(args, want) {
			t.Errorf("the dispatcher lacks %s: %v", want, args)
		}
	}
	real := in.standaloneDispatcherArgs(lifecycleChoice(Options{Agents: AgentOptions{Coder: "claude"}}, manifest.ModeStandalone), false)
	if strings.Contains(strings.Join(real, " "), "--fake-agent") {
		t.Errorf("a real coding agent is given the scripted agent's patch: %v", real)
	}
}

// A release whose real traffic is too thin is judged by the application's
// synthetic suite: an install whose manifest names one builds it next to
// the application and gives it to the supervisor; one that names none
// gives none, and such a release holds.
func TestTheSupervisorRunsTheSyntheticSuiteTheManifestNames(t *testing.T) {
	t.Parallel()
	if !slices.ContainsFunc(standalonePrograms, func(p program) bool {
		return p.file == syntheticProgram && p.pkg == "./examples/hello-app/cmd/hello-app-synthetic" && len(p.units) == 0
	}) {
		t.Errorf("the install does not build the synthetic suite: %+v", standalonePrograms)
	}
	in, _, _ := newTestInstaller(t)
	c := lifecycleChoice(Options{}, manifest.ModeStandalone)
	m := &manifest.Manifest{FactoryID: "sisf-shop"}
	m.Release.LowTraffic.SyntheticSuite = "synthetic"
	want := "--synthetic-program=" + in.Layout.Bin(syntheticProgram)
	if args := in.standaloneSupervisorArgs(m, c); !slices.Contains(args, want) {
		t.Errorf("the supervisor lacks %s: %v", want, args)
	}
	m.Release.LowTraffic.SyntheticSuite = ""
	if args := strings.Join(in.standaloneSupervisorArgs(m, c), " "); strings.Contains(args, "--synthetic-program") {
		t.Errorf("a manifest with no synthetic suite runs one: %s", args)
	}
}
