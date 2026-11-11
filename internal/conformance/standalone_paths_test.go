package conformance

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// remount moves the first mount of a unit to another directory.
func remount(m *manifest.Manifest, name manifest.UnitName, dir string) {
	editUnit(m, name, func(u *manifest.Unit) { u.Mounts[0].Path = dir })
}

// In Standalone mode a mount is a directory on the one machine. A volume
// placed in, on or around the trust volume's directory is the trust volume.
func TestInStandaloneModeNoVolumeSharesADirectoryWithTheTrustVolume(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		unit manifest.UnitName
		dir  string
	}{
		{"the runner's workspace is the trust directory", manifest.UnitRunner, "data/trust"},
		{"the runner's workspace lies inside the trust directory", manifest.UnitRunner, "data/trust/ws"},
		{"the same directory written another way", manifest.UnitRunner, "./data/app/../trust/"},
		{"the QA workspace lies inside the trust directory", manifest.UnitQARunner, "data/trust/qa"},
		{"the application's data lies inside the trust directory", manifest.UnitApp, "data/trust/app"},
		{"a volume of Zone 3 itself lies inside the trust directory", manifest.UnitControl, "data/trust/control"},
		{"the state store's volume lies inside the trust directory", manifest.UnitState, "data/trust/state"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := standalone(t)
			remount(m, tc.unit, tc.dir)
			if tc.unit == manifest.UnitState {
				m.Storage.State.Path = tc.dir + "/factory.db"
			}
			report := Check(m)
			if !report.Has(RuleTrustVolumeExposed) || report.SelfUpdate {
				t.Fatalf("want the trust volume reported as exposed and self-update disabled, got %+v", report)
			}
			if tc.unit != manifest.UnitState {
				wantOnly(t, report, RuleTrustVolumeExposed, "units."+string(tc.unit)+".mounts[0].path")
			}
		})
	}

	// A directory that holds the trust directory holds every other volume
	// too: both the trust volume and the other owners are reported.
	m := standalone(t)
	remount(m, manifest.UnitRunner, "data")
	report := Check(m)
	if !report.Has(RuleTrustVolumeExposed) || !report.Has(RuleVolumePathOverlap) || report.SelfUpdate {
		t.Fatalf("a workspace around every data directory exposes the trust volume and the other zones, got %+v", report.Findings)
	}
}

func TestInStandaloneModeVolumesOfDifferentOwnersDoNotShareADirectory(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		unit manifest.UnitName
		dir  string
		at   string // the mount the finding stands at: the later of the two
	}{
		{"an agent workspace inside the application's data", manifest.UnitRunner, "data/app/ws", "units.runner.mounts[0].path"},
		{"an agent workspace that is the application's data", manifest.UnitRunner, "data/app", "units.runner.mounts[0].path"},
		{"the application's data inside Zone 3's", manifest.UnitApp, "data/control/app", "units.control.mounts[0].path"},
		{"the QA workspace inside the state store's volume", manifest.UnitQARunner, "data/state/qa", "units.state.mounts[0].path"},
		{"Zone 3's data inside the artifact store", manifest.UnitControl, "data/artifacts/control", "units.artifacts.mounts[0].path"},
		{"the artifact store inside the state store's volume", manifest.UnitArtifacts, "data/state/artifacts", "units.artifacts.mounts[0].path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := standalone(t)
			remount(m, tc.unit, tc.dir)
			report := Check(m)
			wantOnly(t, report, RuleVolumePathOverlap, tc.at)
			if len(report.Findings) != 1 {
				t.Fatalf("one misplaced directory is one finding, got %+v", report.Findings)
			}
		})
	}
}

func TestDirectoriesThatKeepTheOwnersApartAreAllowed(t *testing.T) {
	t.Parallel()
	allowed := map[string]func(m *manifest.Manifest){
		"a directory whose name starts like another's": func(m *manifest.Manifest) { remount(m, manifest.UnitRunner, "data/app-workspaces") },
		"an absolute directory beside the others":      func(m *manifest.Manifest) { remount(m, manifest.UnitRunner, "/srv/sisf/workspaces") },
		"two volumes of one zone, one inside the other": func(m *manifest.Manifest) {
			m.Volumes["control-cache"] = manifest.Volume{Zone: core.ZoneControl}
			editUnit(m, manifest.UnitControl, func(u *manifest.Unit) {
				u.Mounts = append(u.Mounts, manifest.Mount{Volume: "control-cache", Path: "data/control/cache"})
			})
		},
		"one volume mounted by two units of its zone": func(m *manifest.Manifest) {
			editUnit(m, manifest.UnitDispatcher, func(u *manifest.Unit) {
				u.Mounts = append(u.Mounts, manifest.Mount{Volume: "workspaces", Path: "data/workspaces"})
			})
		},
	}
	for name, change := range allowed {
		m := standalone(t)
		change(m)
		if report := Check(m); !report.Conformant() {
			t.Errorf("%s must be allowed, got %+v", name, report.Findings)
		}
	}

	// In Docker mode a mount path is a place inside one container: two
	// units mounting different volumes at the same path share nothing.
	m := docker(t)
	if m.Units[manifest.UnitRunner].Mounts[0].Path != m.Units[manifest.UnitQARunner].Mounts[0].Path {
		t.Fatal("the docker example must mount both workspaces at the same container path")
	}
	remount(m, manifest.UnitApp, m.Units[manifest.UnitSupervisor].Mounts[0].Path)
	if report := Check(m); !report.Conformant() {
		t.Fatalf("container paths are not compared across units, got %+v", report.Findings)
	}
}

func TestADirectoryIsWithinAnotherWhenItIsThatDirectoryOrBelowIt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		location, root string
		want           bool
	}{
		{"data/trust", "data/trust", true},
		{"data/trust/ws", "data/trust", true},
		{"data/trust/../trust/ws", "./data/trust/", true},
		{"data/trust-archive", "data/trust", false},
		{"data", "data/trust", false},
		{"/var/lib/sisf/trust/keys", "/var/lib/sisf/trust", true},
		{"/var/lib/sisf", "/", true},
		{"data/trust", ".", true}, // the working directory holds every relative path
		{"../elsewhere", ".", false},
		{"/var/lib/sisf", ".", false},
		// A directory above the working directory holds every relative path.
		{"data/trust", "..", true},
		{"data/trust", "../..", true},
		{"..", "../..", true},
		{"data/trust", "data/../..", true},
		{"/var/lib/sisf", "..", false},
		// The root of the file system holds every path, relative or not.
		{"data/trust", "/", true},
		{"../elsewhere", "/", true},
		{"data/trust", "/var/..", true},
	}
	for _, tc := range cases {
		if got := within(tc.location, tc.root); got != tc.want {
			t.Errorf("within(%q, %q) = %v, want %v", tc.location, tc.root, got, tc.want)
		}
	}
	if !overlap("data", "data/trust") || !overlap("data/trust", "data") || overlap("data/app", "data/trust") {
		t.Error("two directories overlap exactly when one is within the other")
	}
}
