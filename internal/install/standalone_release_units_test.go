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
		"--deploy-local-program=" + r.in.Layout.Bin(appLauncher), "--factory-manifest=" + r.in.Layout.Config(),
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
		!strings.Contains(string(script), "exec '"+r.in.Layout.Bin("hello-app")+"'") {
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
