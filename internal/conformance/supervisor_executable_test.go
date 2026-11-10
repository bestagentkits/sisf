package conformance

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// In Standalone mode the supervisor's image is a binary on the machine. It
// stands for the supervisor whether or not the unit repeats it as a command.
func TestInStandaloneModeTheSupervisorBinaryIsNoOtherUnitsExecutable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		breach func(m *manifest.Manifest)
		path   string
	}{
		{"the supervisor names the factory binary as its image and has no command",
			func(m *manifest.Manifest) {
				m.Supervisor.Image = "bin/factoryd"
				editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Image, u.Command = "bin/factoryd", nil })
			}, "units.control.command"},
		{"the supervisor block alone names the factory binary",
			func(m *manifest.Manifest) {
				m.Supervisor.Image = "bin/factoryd"
				editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Image, u.Command = "", nil })
			}, "units.dispatcher.command"},
		{"another unit starts the supervisor binary by a path that cleans to it",
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Command = []string{"./bin/../bin/sisf-supervisor", "serve"} })
			}, "units.app.command"},
		{"another unit names the supervisor binary as its image, from another directory",
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitEdge, func(u *manifest.Unit) { u.Image = "/opt/sisf/bin/sisf-supervisor" })
			}, "units.edge.image"},
		{"another unit starts through a shell, which hides what it runs",
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Command = []string{"/bin/sh", "-c", "bin/sisf-supervisor serve"} })
			}, "units.app.command"},
		{"another unit starts through env, which hides what it runs",
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitDispatcher, func(u *manifest.Unit) { u.Command = []string{"/usr/bin/env", "bin/sisf-supervisor"} })
			}, "units.dispatcher.command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := standalone(t)
			tc.breach(m)
			report := Check(m)
			wantOnly(t, report, RuleSupervisorSharesArtifact, tc.path)
			if report.SelfUpdate {
				t.Error("a supervisor that shares its executable disables self-update")
			}
		})
	}
}

// A shell or launcher in front of the supervisor names the executable in an
// argument, so the pinned digest says nothing about what runs.
func TestASupervisorStartedThroughAShellIsNotPinned(t *testing.T) {
	t.Parallel()
	wrapped := [][]string{
		{"/bin/sh", "-c", "bin/factoryd supervisor"},
		{"bash", "-lc", "exec bin/sisf-supervisor"},
		{"/usr/bin/env", "bin/factoryd", "supervisor"},
		{"sudo", "bin/sisf-supervisor"},
		{"busybox", "sh", "-c", "bin/sisf-supervisor"},
	}
	for _, command := range wrapped {
		t.Run(command[0], func(t *testing.T) {
			t.Parallel()
			m := standalone(t)
			editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Command = command })
			report := Check(m)
			wantOnly(t, report, RuleSupervisorNotPinned, "units.supervisor.command")
			if len(report.Findings) != 1 || report.SelfUpdate {
				t.Fatalf("want one finding and self-update disabled, got %+v", report)
			}
		})
	}
	// A unit that names no image is identified by what it starts in any mode.
	m := docker(t)
	m.Supervisor.Image = ""
	editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Image, u.Command = "", []string{"sh", "-c", "supervisor"} })
	if report := Check(m); !report.Has(RuleSupervisorNotPinned) || !report.reportedAt(RuleSupervisorNotPinned, "units.supervisor.command") {
		t.Fatalf("a supervisor without an image that starts through a shell is not pinned, got %+v", report.Findings)
	}
}

// Units of a Docker deployment that run different images are different
// artifacts, even when each starts a launcher or interpreter of one name.
func TestUnitsWithImagesOfTheirOwnMayStartALauncherOfTheSameName(t *testing.T) {
	t.Parallel()
	shared := [][2][]string{
		{{"/sbin/tini", "--", "supervisor"}, {"/sbin/tini", "--", "node", "server.js"}},
		{{"node", "supervisor.js"}, {"node", "server.js"}},
		{{"/bin/sh", "-c", "exec supervisor"}, {"/bin/sh", "-c", "exec app"}},
		{{"/usr/bin/factoryd", "supervisor"}, {"factoryd", "serve"}},
	}
	for _, commands := range shared {
		m := docker(t)
		editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Command = commands[0] })
		for _, unit := range []manifest.UnitName{manifest.UnitApp, manifest.UnitControl, manifest.UnitEdge} {
			editUnit(m, unit, func(u *manifest.Unit) { u.Command = commands[1] })
		}
		if report := Check(m); !report.Conformant() || !report.SelfUpdate {
			t.Errorf("images of their own starting %q must be allowed, got %+v", commands[0][0], report.Findings)
		}
	}

	// Without an image of its own, a unit is what it starts.
	m := docker(t)
	editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Command = []string{"sisf-supervisor"} })
	editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Image, u.Command = "", []string{"/usr/local/bin/sisf-supervisor", "serve"} })
	wantOnly(t, Check(m), RuleSupervisorSharesArtifact, "units.app.command")
}

func TestAnImageIsOneRepositoryUnderEveryNameOfItsRegistry(t *testing.T) {
	t.Parallel()
	same := map[string][]string{
		"docker.io/acme/x": {"acme/x", "acme/x:v2", "index.docker.io/acme/x", "registry-1.docker.io/acme/x", "docker.io/acme/x/",
			"docker://docker.io/acme/x", "DOCKER.IO/Acme/X", "docker.io//acme/x", " acme/x@sha256:abc "},
		"docker.io/library/postgres":     {"postgres", "postgres:16", "library/postgres", "docker.io/postgres", "index.docker.io/library/postgres"},
		"ghcr.io/acme/sisf-supervisor":   {"ghcr.io/acme/sisf-supervisor:v2", "oci://ghcr.io/acme/sisf-supervisor", "GHCR.io/acme/sisf-supervisor/"},
		"localhost:5000/sisf-supervisor": {"localhost:5000/sisf-supervisor:v2", "localhost:5000/sisf-supervisor"},
	}
	for want, spellings := range same {
		for _, image := range append(spellings, want) {
			if got := canonicalRepository(image); got != want {
				t.Errorf("canonicalRepository(%q) = %q, want %q", image, got, want)
			}
		}
	}
	different := [][2]string{
		{"acme/x", "acme/y"},
		{"acme/x", "ghcr.io/acme/x"},
		{"docker.io/acme/x", "docker.io/other/x"},
		{"postgres", "acme/postgres"},
	}
	for _, pair := range different {
		if canonicalRepository(pair[0]) == canonicalRepository(pair[1]) {
			t.Errorf("%q and %q are different repositories", pair[0], pair[1])
		}
	}
	if canonicalRepository("") != "" || canonicalRepository(" / ") != "" {
		t.Error("no image is no repository")
	}
}

func TestAUnitRunningTheSupervisorRepositoryUnderARegistryAliasSharesItsArtifact(t *testing.T) {
	t.Parallel()
	for _, alias := range []string{"acme/x", "index.docker.io/acme/x", "docker.io/acme/x/", "docker://docker.io/acme/x", "registry-1.docker.io/acme/x:v2"} {
		t.Run(alias, func(t *testing.T) {
			t.Parallel()
			m := docker(t)
			m.Supervisor.Image = "docker.io/acme/x"
			editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Image = "docker.io/acme/x" })
			editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Image = alias })
			report := Check(m)
			wantOnly(t, report, RuleSupervisorSharesArtifact, "units.app.image")
			if report.SelfUpdate {
				t.Error("a shared repository disables self-update")
			}
		})
	}
}
