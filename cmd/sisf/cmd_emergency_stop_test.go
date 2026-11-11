package main

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/core"
)

// fakeAdmin is a supervisor admin socket on an ephemeral loopback port that
// serves factory and counts the stops it receives.
func fakeAdmin(t *testing.T, factory core.FactoryID) (addr string, stops *atomic.Int32) {
	t.Helper()
	return fakeAdminOfKey(t, factory, "")
}

// fakeAdminOfKey is fakeAdmin for a supervisor whose release is signed by
// the root key keyID.
func fakeAdminOfKey(t *testing.T, factory core.FactoryID, keyID string) (addr string, stops *atomic.Int32) {
	t.Helper()
	stops = &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/status":
			_ = json.NewEncoder(w).Encode(supervisor.Status{FactoryID: factory, SelfCheck: supervisor.SelfCheckStatus{Passed: true, ManifestKeyID: keyID}})
		case r.URL.Path == supervisor.EmergencyStopPath && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(supervisor.FreezeStatus{Frozen: true, StopID: "stop-of-" + string(factory)})
		case r.URL.Path == supervisor.EmergencyStopPath:
			stops.Add(1)
			_ = json.NewEncoder(w).Encode(supervisor.EmergencyStopReport{StopID: "stop-1", PublishingFrozen: true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://"), stops
}

func TestEmergencyStopNamesOnlyWhatIsMissing(t *testing.T) {
	t.Setenv("USER", "")
	t.Setenv("SISF_STATE_DSN", "")
	status, _, stderr := runWithOutput(t, "emergency-stop", "--factory-id", "sisf-hello", "--reason", "test", "--admin", "none")
	if status != exitUsage || !strings.Contains(stderr, "pass --actor, since $USER is not set") {
		t.Fatalf("emergency-stop = %d %q, want the missing actor named", status, stderr)
	}
	if strings.Contains(stderr, "--factory-id,") || strings.Contains(stderr, "pass --reason") {
		t.Errorf("flags that were given are named as missing: %q", stderr)
	}
}

// A stop with no route to the factory is a missing input like the others:
// it exits 2 and says what to give.
func TestEmergencyStopWithNoRouteIsAMissingInput(t *testing.T) {
	t.Setenv("SISF_STATE_DSN", "")
	status, _, stderr := runWithOutput(t, "emergency-stop", "--factory-id", "sisf-hello", "--reason", "test", "--actor", "user:t", "--admin", "none")
	if status != exitUsage || !strings.Contains(stderr, "set $SISF_STATE_DSN") || !strings.Contains(stderr, "--admin") {
		t.Fatalf("emergency-stop without a store or a supervisor = %d %q, want exit 2 naming both routes", status, stderr)
	}
}

func TestEmergencyStopIsNeverSentToTheSupervisorOfAnotherFactory(t *testing.T) {
	t.Setenv("SISF_STATE_DSN", "")
	dir := initSim(t, "standalone")
	addr, stops := fakeAdmin(t, "sisf-other")
	status, stdout, stderr := runWithOutput(t, "emergency-stop", "--dir", dir, "--reason", "drill", "--actor", "user:tester", "--admin", addr)
	if stops.Load() != 0 {
		t.Fatal("the stop was sent to the supervisor of another factory")
	}
	if status != exitFailure || !strings.Contains(stdout, "serves factory \\\"sisf-other\\\"") || !strings.Contains(stdout, "nothing was sent") {
		t.Fatalf("emergency-stop = %d\nstdout: %s\nstderr: %s", status, stdout, stderr)
	}

	// --status asks the same question first and answers for no other factory.
	status, stdout, stderr = runWithOutput(t, "emergency-stop", "--dir", dir, "--status", "--admin", addr)
	if status != exitFailure || strings.Contains(stdout, "stop-of-sisf-other") || !strings.Contains(stderr, "nothing was sent") {
		t.Fatalf("emergency-stop --status = %d\nstdout: %s\nstderr: %s", status, stdout, stderr)
	}

	// The supervisor of this install gets the stop.
	own, ownStops := fakeAdmin(t, "sisf-hello-app")
	runWithOutput(t, "emergency-stop", "--factory-id", "sisf-hello-app", "--reason", "drill", "--actor", "user:tester", "--admin", own)
	if ownStops.Load() != 1 {
		t.Fatalf("the supervisor of the factory named got %d stops", ownStops.Load())
	}
}

func TestEmergencyStopReadsTheFactoryFromTheInstall(t *testing.T) {
	t.Setenv("SISF_STATE_DSN", "")
	dir := initSim(t, "standalone")
	status, _, stderr := runWithOutput(t, "emergency-stop", "--dir", dir, "--factory-id", "sisf-other", "--reason", "x", "--actor", "user:t")
	if status != exitUsage || !strings.Contains(stderr, "factory sisf-hello-app") {
		t.Fatalf("a --factory-id that differs from the install = %d %q", status, stderr)
	}
	docker := initSim(t, "docker")
	status, _, stderr = runWithOutput(t, "emergency-stop", "--dir", docker, "--reason", "x", "--actor", "user:t")
	if status != exitFailure || !strings.Contains(stderr, "Docker mode") || !strings.Contains(stderr, "factory.emergency_stop") {
		t.Fatalf("a Docker install = %d %q, want the routes that work there", status, stderr)
	}
}

// installTree lists every path under dir, relative to it.
func installTree(t *testing.T, dir string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		paths = append(paths, rel)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// An install that was never started has no state store. A stop or a lift
// reports that and creates none, and the stop still reaches the supervisor.
func TestEmergencyStopOnAnInstallNeverStartedCreatesNothing(t *testing.T) {
	t.Setenv("SISF_STATE_DSN", "")
	dir := initSim(t, "standalone")
	before := installTree(t, dir)
	target, err := install.ReadStopTarget(dir)
	if err != nil {
		t.Fatal(err)
	}
	addr, stops := fakeAdminOfKey(t, target.FactoryID, target.RootKeyID)
	for _, tc := range []struct {
		args      []string
		wantStops int32
	}{
		{[]string{"--admin", "none"}, 0},
		{[]string{"--admin", addr}, 1},
		{[]string{"--admin", "none", "--lift"}, 1},
	} {
		args := append([]string{"emergency-stop", "--dir", dir, "--reason", "drill", "--actor", "user:tester"}, tc.args...)
		status, stdout, stderr := runWithOutput(t, args...)
		if status != exitFailure || !strings.Contains(stdout+stderr, "the state store is not prepared") {
			t.Errorf("%q = %d\nstdout: %s\nstderr: %s", tc.args, status, stdout, stderr)
		}
		if got := stops.Load(); got != tc.wantStops {
			t.Errorf("%q: the supervisor got %d stops, want %d", tc.args, got, tc.wantStops)
		}
		if after := installTree(t, dir); !slices.Equal(after, before) {
			t.Fatalf("%q changed the install:\nbefore %q\nafter  %q", tc.args, before, after)
		}
	}
}
