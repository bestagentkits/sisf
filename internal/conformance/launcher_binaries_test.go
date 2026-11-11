package conformance

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// A unit whose binary no rule names, other than the application, is not a
// process `sisf up` starts from bin/: in a Standalone install the state and
// artifacts units may start none of its programs, the application launcher
// least of all, since it runs what its arguments name.
func TestAUnitWithoutABinaryOfItsOwnStartsNoProgramOfTheInstall(t *testing.T) {
	t.Parallel()
	for _, unit := range []manifest.UnitName{manifest.UnitState, manifest.UnitArtifacts} {
		for _, command := range [][]string{
			{"bin/sisf-app-launcher"},
			{"bin/sisf-app-launcher", "x", "/tmp/evil"},
			{"bin/hello-app"},
			{"bin/factoryd", "control"},
		} {
			m := standalone(t)
			editUnit(m, unit, func(u *manifest.Unit) {
				u.Command, u.Digest = command, "sha256:8888888888888888888888888888888888888888888888888888888888888888"
			})
			// Other rules may refuse such a unit too; this one must.
			if report := Check(m); report.Conformant() || !report.reportedAt(RuleUnitThroughLauncher, "units."+string(unit)+".command") {
				t.Errorf("%s starting %q: want %s, got %+v", unit, command, RuleUnitThroughLauncher, report.Findings)
			}
		}
	}
}

// A file system may drop the spaces around a file name, and a launcher
// named with them is not recognized by its name: a command any word of
// which names a file with whitespace around it is refused, in both modes.
func TestACommandNamingAFileWithSurroundingWhitespaceIsRefused(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		unit    manifest.UnitName
		command []string
	}{
		{manifest.UnitApp, []string{"bin/sisf-app-launcher ", "x", "/tmp/evil"}},
		{manifest.UnitApp, []string{"bin/factoryd\t", "control"}},
		{manifest.UnitApp, []string{"bin/ hello-app"}},
		{manifest.UnitApp, []string{"bin/hello-app "}},
		{manifest.UnitControl, []string{"bin/factoryd ", "control"}},
	} {
		m := standalone(t)
		editUnit(m, tc.unit, func(u *manifest.Unit) { u.Command = tc.command })
		report := Check(m)
		if report.Conformant() || !report.reportedAt(RuleUnitThroughLauncher, "units."+string(tc.unit)+".command") {
			t.Errorf("%s starting %q: want %s, got %+v", tc.unit, tc.command, RuleUnitThroughLauncher, report.Findings)
		}
	}
	m := docker(t)
	editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Image, u.Command = "", []string{"sh ", "-c", "serve"} })
	wantOnly(t, Check(m), RuleUnitThroughLauncher, "units.app.command")

	// Any word of the command, not only the first, may name a program.
	m = docker(t)
	editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Image, u.Command = "", []string{"tini", "--", "bin/sisf-supervisor "} })
	if report := Check(m); report.Conformant() || !report.reportedAt(RuleUnitThroughLauncher, "units.app.command") {
		t.Errorf("a padded later word: want %s, got %+v", RuleUnitThroughLauncher, report.Findings)
	}
	m = standalone(t)
	editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Command = []string{"bin/hello-app", "--config", "data/app/config "} })
	wantOnly(t, Check(m), RuleUnitThroughLauncher, "units.app.command")
}

// The supervisor's binary is the factory's whatever image the manifest
// names for the supervisor: the application unit may not start
// bin/sisf-supervisor even when supervisor.image names another file.
func TestTheSupervisorBinaryIsAlwaysTheFactorys(t *testing.T) {
	t.Parallel()
	for _, command := range [][]string{{"bin/sisf-supervisor"}, {"bin/SISF-Supervisor", "--serve"}} {
		m := standalone(t)
		m.Supervisor.Image = "bin/acme-supervisor"
		editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) {
			u.Image, u.Command = "bin/acme-supervisor", []string{"bin/acme-supervisor"}
		})
		if report := Check(m); !report.Conformant() {
			t.Fatalf("a supervisor named bin/acme-supervisor must be accepted on its own, got %+v", report.Findings)
		}
		editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Command = command })
		wantOnly(t, Check(m), RuleUnitThroughLauncher, "units.app.command")
	}
}
