package conformance

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// A unit identified by what it starts, as every unit of a Standalone
// deployment is, hides what runs when a shell or launcher comes first. It is
// refused for that, under a rule that says so, and the supervisor boundary
// is not shown to hold while what such a unit runs is unknown.
func TestAUnitIdentifiedByWhatItStartsDoesNotStartThroughALauncher(t *testing.T) {
	t.Parallel()
	wrapped := [][]string{
		{"sh", "-c", "bin/hello-app"},
		{"/bin/bash", "-lc", "exec bin/hello-app"},
		{"/usr/bin/env", "bin/hello-app"},
		{"nohup", "bin/hello-app"},
	}
	for _, command := range wrapped {
		t.Run(command[0], func(t *testing.T) {
			t.Parallel()
			m := standalone(t)
			editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Command = command })
			report := Check(m)
			wantOnly(t, report, RuleUnitThroughLauncher, "units.app.command")
			if len(report.Findings) != 1 || report.SelfUpdate {
				t.Fatalf("want one finding and self-update disabled, got %+v", report)
			}
		})
	}

	// Without a supervisor the launcher still hides what runs from the
	// directory rules.
	m := standalone(t)
	m.Supervisor, m.Release.Units = nil, nil
	editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Command, u.Image, u.Mounts = nil, "", nil })
	delete(m.Volumes, "trust")
	editUnit(m, manifest.UnitControl, func(u *manifest.Unit) { u.Command = []string{"sh", "-c", "python3 data/workspaces/c.py"} })
	if report := Check(m); !report.reportedAt(RuleUnitThroughLauncher, "units.control.command") {
		t.Fatalf("a launcher hides what runs whether or not a supervisor is declared, got %+v", report.Findings)
	}

	// A Docker unit without an image of its own is what it starts.
	m = docker(t)
	editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Image, u.Command = "", []string{"sh", "-c", "serve"} })
	wantOnly(t, Check(m), RuleUnitThroughLauncher, "units.app.command")
}

// A file system that ignores letter case runs /bin/sh for /bin/SH, so the
// name of a launcher is recognized in any case, in both modes.
func TestALauncherIsRecognizedWhateverTheCaseOfItsName(t *testing.T) {
	t.Parallel()
	for _, command := range [][]string{
		{"/bin/SH", "-c", "exec bin/hello-app"},
		{"BASH", "-c", "bin/hello-app"},
		{"/usr/bin/Env", "bin/hello-app"},
	} {
		t.Run(command[0], func(t *testing.T) {
			t.Parallel()
			m := standalone(t)
			editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Command = command })
			wantOnly(t, Check(m), RuleUnitThroughLauncher, "units.app.command")

			m = docker(t)
			editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Image, u.Command = "", command })
			wantOnly(t, Check(m), RuleUnitThroughLauncher, "units.app.command")
		})
	}
	m := standalone(t)
	editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Command = []string{"/bin/SH", "-c", "bin/sisf-supervisor"} })
	if report := Check(m); report.Conformant() || report.SelfUpdate {
		t.Fatalf("a supervisor started through /bin/SH was accepted: %+v", report)
	}
}

// No list of launchers is complete: an interpreter runs whatever program its
// arguments name, here the supervisor's executable under a name no check can
// read. In Standalone mode a unit must therefore start a factory binary from
// the install's bin/ directory, and anything else is refused.
func TestAStandaloneUnitStartsOnlyAFactoryBinaryFromTheInstallsBinDirectory(t *testing.T) {
	t.Parallel()
	hidden := "import os; os.execv('bin/sisf-' + 'supervisor', ['supervisor'])"
	refused := [][]string{
		{"python3", "-c", hidden},
		{"/usr/bin/python3", "-c", hidden},
		{"perl", "-e", "exec 'bin/sisf-'.'supervisor'"},
		{"node", "-e", "require('child_process').spawn('bin/sisf-' + 'supervisor')"},
		{"/usr/local/bin/factoryd", "control"},
		{"/opt/factory/bin/factoryd", "control"},
		{"./bin/factoryd", "control"},
		{"bin/../bin/factoryd", "control"},
		{"bin/tools/factoryd", "control"},
		{"factoryd", "control"},
		{"bin/", "control"},
		{"BIN/factoryd", "control"},
	}
	for _, unit := range []manifest.UnitName{manifest.UnitApp, manifest.UnitControl, manifest.UnitDispatcher, manifest.UnitEdge, manifest.UnitRunner, manifest.UnitQARunner} {
		for _, command := range refused {
			m := standalone(t)
			editUnit(m, unit, func(u *manifest.Unit) { u.Command = command })
			report := Check(m)
			if report.Conformant() || !report.reportedAt(RuleUnitThroughLauncher, "units."+string(unit)+".command") {
				t.Errorf("%s starting %q: want %s, got %+v", unit, command, RuleUnitThroughLauncher, report.Findings)
			}
		}
	}

	// The supervisor, which has rules of its own, is refused too.
	m := standalone(t)
	editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Command = []string{"python3", "-c", hidden} })
	if report := Check(m); report.Conformant() || report.SelfUpdate {
		t.Fatalf("a supervisor started through an interpreter was accepted: %+v", report)
	}

	// In Docker mode a unit with an image of its own is identified by it.
	m = docker(t)
	editUnit(m, manifest.UnitControl, func(u *manifest.Unit) { u.Command = []string{"python3", "-c", "serve()"} })
	if report := Check(m); !report.Conformant() {
		t.Fatalf("a Docker unit is identified by its image, got %+v", report.Findings)
	}
}
