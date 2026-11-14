package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// manifestArg is the factory manifest the supervisor is started with.
func manifestArg(t *testing.T, args []string) string {
	t.Helper()
	at := slices.Index(args, "--factory-manifest")
	if at < 0 || at+1 >= len(args) {
		t.Fatal("the supervisor is started without a factory manifest")
	}
	return args[at+1]
}

// appendToManifest adds YAML to the manifest the supervisor is started with.
func appendToManifest(t *testing.T, args []string, yaml string) {
	t.Helper()
	path := manifestArg(t, args)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, []byte(yaml)...), 0o600); err != nil {
		t.Fatal(err)
	}
}

// withoutRoot starts the supervisor without the root private key, so it
// reads the delegated keys the trust volume holds instead of issuing them.
func withoutRoot(a []string) []string { return a[:len(a)-2] }

// writeTrustFile writes a file of the trust volume with the given mode.
func writeTrustFile(t *testing.T, i *install, name, content string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(i.dir, "trust", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// Every refusal a flag, the manifest or a file's content decides ends with
// EX_CONFIG, which `sisf up` does not restart on: the same configuration
// would be refused again. A file that cannot be read, or is not there,
// may be at the next start, and ends with a failure `sisf up` restarts.
func TestAStartTheConfigurationRefusesEndsWithExConfig(t *testing.T) {
	t.Parallel()
	qaKey := delegationFile(delegation.PurposeQAReceipt)
	for name, c := range map[string]struct {
		change func(t *testing.T, i *install, args []string) []string
		status int
	}{
		"no edge admin URL": {func(_ *testing.T, _ *install, a []string) []string {
			return append(a, "--edge-admin-url", "")
		}, exitConfigRefused},
		"an edge admin URL that is not one": {func(_ *testing.T, _ *install, a []string) []string {
			return append(a, "--edge-admin-url", "not a url")
		}, exitConfigRefused},
		"an unknown deploy backend": {func(_ *testing.T, _ *install, a []string) []string {
			return append(a, "--deploy", "kubernetes")
		}, exitConfigRefused},
		"a release window of zero": {func(_ *testing.T, _ *install, a []string) []string {
			return append(a, "--release-window", "0s")
		}, exitConfigRefused},
		"no release samples": {func(_ *testing.T, _ *install, a []string) []string {
			return append(a, "--release-min-samples", "0")
		}, exitConfigRefused},
		"a relative synthetic program": {func(_ *testing.T, _ *install, a []string) []string {
			return append(a, "--synthetic-program", "suite")
		}, exitConfigRefused},
		"a manifest that does not match the schema": {func(t *testing.T, _ *install, a []string) []string {
			appendToManifest(t, a, "unknown_section: true\n")
			return a
		}, exitConfigRefused},
		"a delegation that is not JSON": {func(t *testing.T, i *install, a []string) []string {
			writeTrustFile(t, i, qaKey, "not a delegation", 0o600)
			return withoutRoot(a)
		}, exitConfigRefused},
		"an empty delegation": {func(t *testing.T, i *install, a []string) []string {
			writeTrustFile(t, i, qaKey, "", 0o600)
			return withoutRoot(a)
		}, exitConfigRefused},
		// What the environment decides may change by the next start.
		"a synthetic program not there yet": {func(t *testing.T, i *install, a []string) []string {
			return append(a, "--synthetic-program", filepath.Join(i.dir, "no-suite"))
		}, exitFailure},
		"a manifest not there yet": {func(t *testing.T, i *install, a []string) []string {
			return append(a, "--factory-manifest", filepath.Join(i.dir, "no-factory.yaml"))
		}, exitFailure},
		"a delegation that cannot be read": {func(t *testing.T, i *install, a []string) []string {
			if os.Geteuid() == 0 {
				t.Skip("root reads a file whatever its mode")
			}
			writeTrustFile(t, i, qaKey, "{}", 0o000)
			return withoutRoot(a)
		}, exitFailure},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inst, args := lifecycleInstall(t)
			status, _, stderr := invoke(inst.lookupEnv, c.change(t, inst, args)...)
			if status != c.status || !strings.Contains(stderr, "lifecycle") {
				t.Fatalf("status %d, stderr %q, want status %d and a refusal naming the lifecycle", status, stderr, c.status)
			}
			if inst.socketExists() {
				t.Error("a refused supervisor opened its admin socket")
			}
		})
	}
}

// The refusals the wiring reaches only past the simulation profile or a
// backend are marked as the configuration's too.
func TestRefusalsPastTheSimulationAreTheConfigurations(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := &manifest.Manifest{FactoryID: testFactory}
	short := releaseTuning{window: 1, minSamples: 1}
	_, shortErr := short.monitor(false)
	_, _, _, relativeErr := (&qaFlags{workspaceRoot: "qa"}).paths(judgeDeps{manifest: m}, "docker")
	_, _, _, overlapErr := (&qaFlags{workspaceRoot: dir}).paths(judgeDeps{manifest: m, protected: []string{dir}}, "docker")
	_, _, _, homeErr := (&qaFlags{workspaceRoot: dir, home: filepath.Join(dir, "home")}).paths(judgeDeps{manifest: m}, "docker")
	_, observerErr := (&lifecycleFlags{coderAgent: "claude"}).artifactObserver(false)
	_, _, launcherErr := (&localPrograms{launcher: "app"}).command("app@sha256:" + strings.Repeat("0", 64))
	for name, err := range map[string]error{
		"a short release window outside the simulation": shortErr,
		"a relative QA workspace":                       relativeErr,
		"a QA workspace over the factory's secrets":     overlapErr,
		"a QA home under docker":                        homeErr,
		"no program store to observe releases":          observerErr,
		"a relative local launcher":                     launcherErr,
	} {
		if err == nil {
			t.Errorf("%s is accepted", name)
			continue
		}
		if got := refusalStatus(err); got != exitConfigRefused {
			t.Errorf("%s ends with %d, want %d: %v", name, got, exitConfigRefused, err)
		}
	}
}
