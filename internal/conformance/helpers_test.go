package conformance

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// docker returns a fresh copy of the conformant Docker manifest.
func docker(t *testing.T) *manifest.Manifest {
	t.Helper()
	return load(t, "testdata/valid-docker.yaml")
}

// standalone returns a fresh copy of the conformant Standalone manifest.
func standalone(t *testing.T) *manifest.Manifest {
	t.Helper()
	return load(t, "testdata/valid-standalone.yaml")
}

func load(t *testing.T, path string) *manifest.Manifest {
	t.Helper()
	m, err := manifest.ParseFile(path)
	if err != nil {
		t.Fatalf("%s must parse: %v", path, err)
	}
	if report := Check(m); !report.Conformant() {
		t.Fatalf("%s must be conformant before a test changes it, got %+v", path, report.Findings)
	}
	return m
}

// editUnit applies change to one unit of m.
func editUnit(m *manifest.Manifest, name manifest.UnitName, change func(u *manifest.Unit)) {
	unit := m.Units[name]
	change(&unit)
	m.Units[name] = unit
}

// mount attaches volume to a unit of m.
func mount(m *manifest.Manifest, name manifest.UnitName, volume string, readOnly bool) {
	editUnit(m, name, func(u *manifest.Unit) {
		u.Mounts = append(u.Mounts, manifest.Mount{Volume: volume, Path: "/mnt/" + volume, ReadOnly: readOnly})
	})
}

// wantOnly fails unless the report holds findings of rule alone, one of them
// at path, each saying what is wrong and how to fix it.
func wantOnly(t *testing.T, report Report, rule Rule, path string) {
	t.Helper()
	if report.Conformant() {
		t.Fatalf("the manifest was accepted; want it rejected by %s at %s", rule, path)
	}
	at := false
	for _, finding := range report.Findings {
		if finding.Rule != rule {
			t.Errorf("unexpected finding [%s] %s: %s", finding.Rule, finding.Path, finding.Message)
		}
		if finding.Message == "" || finding.Fix == "" {
			t.Errorf("finding [%s] at %s must say what is wrong and how to fix it", finding.Rule, finding.Path)
		}
		at = at || finding.Path == path
	}
	if !at {
		t.Errorf("no %s finding at %s, got %+v", rule, path, report.Findings)
	}
	if err := report.Err(); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("a rejected manifest must yield INVALID_ARGUMENT, got %v", err)
	}
}
