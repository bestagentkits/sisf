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
