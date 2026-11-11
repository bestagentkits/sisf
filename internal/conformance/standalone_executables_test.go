package conformance

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// A mount that climbs out of the factory root holds every directory below
// that root: the trust volume, the other zones' data and the agent
// workspaces.
func TestInStandaloneModeNoMountClimbsOutOfTheFactoryRoot(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		unit manifest.UnitName
		dir  string
	}{
		{"the application's data is the parent directory", manifest.UnitApp, ".."},
		{"the application's data is two levels up", manifest.UnitApp, "../.."},
		{"a directory that cleans to the parent", manifest.UnitApp, "data/../../"},
		{"a sibling of the factory root", manifest.UnitApp, "../app-data"},
		{"Zone 3's data is the parent directory", manifest.UnitControl, ".."},
		// The root of the file system holds the factory root, wherever it is.
		{"the application's data is the file system root", manifest.UnitApp, "/"},
		{"a directory that cleans to the file system root", manifest.UnitApp, "//var/.."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := standalone(t)
			remount(m, tc.unit, tc.dir)
			report := Check(m)
			wantOnly(t, report, RuleTrustVolumeExposed, "units."+string(tc.unit)+".mounts[0].path")
			if len(report.Findings) != 1 || report.SelfUpdate {
				t.Fatalf("want one finding and self-update disabled, got %+v", report)
			}
		})
	}

	// A workspace above the factory root also holds the other workspace, so
	// QA independence fails beside the trust volume.
	for _, unit := range []manifest.UnitName{manifest.UnitRunner, manifest.UnitQARunner} {
		m := standalone(t)
		remount(m, unit, "..")
		report := Check(m)
		if !report.reportedAt(RuleTrustVolumeExposed, "units."+string(unit)+".mounts[0].path") ||
			!report.Has(RuleQASharedWorkspace) || report.SelfUpdate {
			t.Errorf("a %s workspace above the factory root exposes the trust volume and the other workspace, got %+v", unit, report.Findings)
		}
	}

	// Without a relative trust directory beneath it, a climbing mount still
	// holds the other zones' directories.
	m := standalone(t)
	remount(m, manifest.UnitSupervisor, "/var/lib/sisf/trust")
	remount(m, manifest.UnitApp, "..")
	report := Check(m)
	wantOnly(t, report, RuleVolumePathOverlap, "units.app.mounts[0].path")
	if len(report.Findings) != 1 || !report.SelfUpdate {
		t.Fatalf("want one isolation finding with the supervisor boundary intact, got %+v", report)
	}

	// The root of the file system also holds an absolute trust directory.
	m = standalone(t)
	remount(m, manifest.UnitSupervisor, "/var/lib/sisf/trust")
	remount(m, manifest.UnitApp, "/")
	if report := Check(m); !report.reportedAt(RuleTrustVolumeExposed, "units.app.mounts[0].path") || report.SelfUpdate {
		t.Fatalf("a mount at the file system root holds an absolute trust directory, got %+v", report.Findings)
	}

	// The trust volume itself placed above the factory root holds the stores
	// and everything else.
	m = standalone(t)
	remount(m, manifest.UnitSupervisor, "..")
	if report := Check(m); !report.reportedAt(RuleTrustVolumeExposed, "units.supervisor.mounts[0].path") || report.SelfUpdate {
		t.Fatalf("a trust directory above the factory root is exposed, got %+v", report.Findings)
	}

	// Container paths are not directories of one machine.
	m = docker(t)
	remount(m, manifest.UnitRunner, "/workspace/..")
	if report := Check(m); !report.Conformant() {
		t.Fatalf("docker mount paths are not compared as host directories, got %+v", report.Findings)
	}
}

// In Standalone mode what the supervisor unit starts is the pinned artifact,
// and no unit can write the directory it lies in.
func TestInStandaloneModeTheSupervisorStartsItsPinnedBinary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		breach func(m *manifest.Manifest)
		path   string
	}{
		{"another binary than the pinned one",
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Command = []string{"bin/other-supervisor"} })
			}, "units.supervisor.command"},
		{"a binary inside the runner's workspace",
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Command = []string{"data/workspaces/out/sup"} })
			}, "units.supervisor.command"},
		{"the pinned binary itself lies inside the runner's workspace",
			func(m *manifest.Manifest) {
				m.Supervisor.Image = "data/workspaces/sisf-supervisor"
				editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Image, u.Command = "", nil })
			}, "supervisor.image"},
		{"the pinned binary lies inside the application's data",
			func(m *manifest.Manifest) {
				m.Supervisor.Image = "./data/app/../app/sisf-supervisor"
				editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) {
					u.Image, u.Command = "", []string{"data/app/sisf-supervisor"}
				})
			}, "units.supervisor.command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := standalone(t)
			tc.breach(m)
			report := Check(m)
			wantOnly(t, report, RuleSupervisorNotPinned, tc.path)
			if report.SelfUpdate {
				t.Error("a supervisor that does not start its pinned binary disables self-update")
			}
		})
	}

	// The same binary written another way, with arguments, is the pinned one.
	m := standalone(t)
	editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Command = []string{"./bin/../bin/sisf-supervisor", "--serve"} })
	if report := Check(m); !report.Conformant() || !report.SelfUpdate {
		t.Fatalf("a path that cleans to the pinned binary must be allowed, got %+v", report.Findings)
	}

	// In Docker mode the command is a path inside the supervisor's own image.
	m = docker(t)
	editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Command = []string{"/usr/local/bin/sisf-supervisor"} })
	if report := Check(m); !report.Conformant() || !report.SelfUpdate {
		t.Fatalf("a docker supervisor command is not compared with its image reference, got %+v", report.Findings)
	}
}

// In Standalone mode a unit that starts a binary from a directory another
// unit mounts runs whatever that unit writes there.
func TestInStandaloneModeNoUnitStartsABinaryFromAnotherUnitsDirectory(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		unit    manifest.UnitName
		command []string
	}{
		{"the qa-runner starts a binary from the coder's workspace", manifest.UnitQARunner, []string{"data/workspaces/bin/qa"}},
		{"the control unit starts a binary from the coder's workspace", manifest.UnitControl, []string{"data/workspaces/factoryd", "control"}},
		{"the edge starts a binary from the application's data", manifest.UnitEdge, []string{"./data/app/edge"}},
		{"the runner starts a binary from the artifact store", manifest.UnitRunner, []string{"data/artifacts/runner"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := standalone(t)
			editUnit(m, tc.unit, func(u *manifest.Unit) { u.Command = tc.command })
			wantOnly(t, Check(m), RuleExecutableInAnotherUnitsDirectory, "units."+string(tc.unit)+".command")
		})
	}

	// An interpreter or a loader runs the file it is given, so every word
	// of the command, and the value after "=" in one, is held to the rule.
	arguments := []struct {
		name    string
		unit    manifest.UnitName
		command []string
		path    string
	}{
		{"control runs a script from the coder's workspace", manifest.UnitControl,
			[]string{"python3", "data/workspaces/control.py"}, "units.control.command[1]"},
		{"control preloads a module from the coder's workspace", manifest.UnitControl,
			[]string{"node", "--require=data/workspaces/hook.js", "bin/control.js"}, "units.control.command[1]"},
		{"the edge loads a plugin from the application's data", manifest.UnitEdge,
			[]string{"bin/sisf-edge", "--plugin", "./data/app/plugin.so"}, "units.edge.command[2]"},
		{"the dispatcher runs a script from the coder's workspace by an absolute path", manifest.UnitDispatcher,
			[]string{"python3", "/home/owner/factory/data/workspaces/dispatch.py"}, "units.dispatcher.command[1]"},
		{"control runs a script given as a file URL", manifest.UnitControl,
			[]string{"deno", "run", "file://data/workspaces/control.ts"}, "units.control.command[2]"},
		// The factory root's name is unknown, so ../factory/data/workspaces
		// may be the coder's workspace.
		{"control runs a script through a path that climbs out of the factory root", manifest.UnitControl,
			[]string{"python3", "../factory/data/workspaces/control.py"}, "units.control.command[1]"},
		{"control starts a binary through a path that climbs out of the factory root", manifest.UnitControl,
			[]string{"../factory/data/workspaces/factoryd"}, "units.control.command"},
	}
	for _, tc := range arguments {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := standalone(t)
			editUnit(m, tc.unit, func(u *manifest.Unit) { u.Command = tc.command })
			report := Check(m)
			wantOnly(t, report, RuleExecutableInAnotherUnitsDirectory, tc.path)
			if len(report.Findings) != 1 {
				t.Fatalf("one argument is one finding, got %+v", report.Findings)
			}
		})
	}

	// The supervisor's later arguments may not name a file another unit
	// writes either; its own trust directory is its own to name.
	m := standalone(t)
	editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) {
		u.Command = []string{"bin/sisf-supervisor", "--policy=data/control/policy.wasm"}
	})
	report := Check(m)
	wantOnly(t, report, RuleSupervisorNotPinned, "units.supervisor.command[1]")
	if report.SelfUpdate {
		t.Error("a supervisor that loads a file another unit writes disables self-update")
	}
	m = standalone(t)
	editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) {
		u.Command = []string{"bin/sisf-supervisor", "--trust-dir", "data/trust"}
	})
	if report := Check(m); !report.Conformant() || !report.SelfUpdate {
		t.Fatalf("the supervisor may name its own trust directory, got %+v", report.Findings)
	}

	// Words that are not files of another unit pass: subcommands, flags,
	// addresses, URLs and the unit's own directory.
	m = standalone(t)
	editUnit(m, manifest.UnitControl, func(u *manifest.Unit) {
		u.Command = []string{"bin/factoryd", "control", "--listen=127.0.0.1:7463", "--upstream", "http://127.0.0.1:7461/data/app",
			"--data", "data/control", "--log-dir=logs/control"}
	})
	if report := Check(m); !report.Conformant() {
		t.Fatalf("arguments outside other units' directories must be allowed, got %+v", report.Findings)
	}

	// A unit may start a binary from a directory only it mounts.
	m = standalone(t)
	editUnit(m, manifest.UnitRunner, func(u *manifest.Unit) { u.Command = []string{"data/workspaces/tool"} })
	if report := Check(m); !report.Conformant() {
		t.Fatalf("a unit's own directory is its own to run from, got %+v", report.Findings)
	}

	// In Docker mode a command is a path inside the unit's own container.
	m = docker(t)
	editUnit(m, manifest.UnitQARunner, func(u *manifest.Unit) { u.Command = []string{"/workspace/bin/qa"} })
	if report := Check(m); !report.Conformant() {
		t.Fatalf("docker commands are not compared with other units' mounts, got %+v", report.Findings)
	}
}

// A unit that names the supervisor's executable anywhere in its command may
// run it, whatever program comes first.
func TestNoOtherUnitNamesTheSupervisorExecutableInItsCommand(t *testing.T) {
	t.Parallel()
	commands := [][]string{
		{"tini", "--", "bin/sisf-supervisor"},
		{"stdbuf", "-o0", "bin/sisf-supervisor"},
		{"python3", "bin/sisf-supervisor"},
		{"/usr/bin/runner", "--exec=./bin/../bin/sisf-supervisor"},
		// Run by its name, found wherever the search path leads.
		{"bin/factoryd", "control", "sisf-supervisor"},
		// The pinned binary under a factory root written out in full.
		{"python3", "/opt/factory/bin/sisf-supervisor"},
		// One file where the file system ignores case.
		{"python3", "BIN/SISF-Supervisor"},
		// The first word is what runs, from whatever directory.
		{"/opt/elsewhere/sisf-supervisor", "control"},
	}
	for _, command := range commands {
		t.Run(strings.Join(command, " "), func(t *testing.T) {
			t.Parallel()
			m := standalone(t)
			editUnit(m, manifest.UnitControl, func(u *manifest.Unit) { u.Command = command })
			report := Check(m)
			wantOnly(t, report, RuleSupervisorSharesArtifact, "units.control.command")
			if report.SelfUpdate {
				t.Error("a unit that can start the supervisor's executable disables self-update")
			}
		})
	}

	// A later word that merely ends in the supervisor's name names another
	// file: a log directory, or a route of a URL.
	allowed := [][]string{
		{"bin/factoryd", "control", "--log-dir", "logs/sisf-supervisor"},
		{"bin/factoryd", "control", "--log-dir=logs/sisf-supervisor"},
		{"bin/factoryd", "control", "--upstream=http://127.0.0.1:7464/sisf-supervisor"},
		{"bin/factoryd", "control", "--upstream", "http://127.0.0.1:7464/bin/sisf-supervisor"},
		{"bin/factoryd", "control", "/opt/elsewhere/sisf-supervisor"},
	}
	for _, command := range allowed {
		m := standalone(t)
		editUnit(m, manifest.UnitControl, func(u *manifest.Unit) { u.Command = command })
		if report := Check(m); !report.Conformant() || !report.SelfUpdate {
			t.Errorf("%q names no file the supervisor starts, got %+v", command, report.Findings)
		}
	}

	// A Docker unit without an image of its own is what it starts.
	m := docker(t)
	editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Command = []string{"sisf-supervisor"} })
	editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Image, u.Command = "", []string{"tini", "--", "sisf-supervisor"} })
	wantOnly(t, Check(m), RuleSupervisorSharesArtifact, "units.app.command")
}
