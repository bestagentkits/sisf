//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A Standalone factory, every unit a local process on SQLite, takes one issue
// the whole way: built and judged as local processes, released by canary to
// local application processes behind the edge, verified, published and
// closed. A second issue of the same unit is released as well, once the
// Owner put a policy that contains the first release in force.
func TestStandaloneReleasesAnIssueTheWholeWay(t *testing.T) {
	d := newStandaloneDeployment(t)
	runFullLifecycle(t, d)
	d.restartSupervisor(t)
	releaseAgainAfterTheOwnerMovesTheBaseline(t, d)
	d.down(t)
	if left := d.installProcesses(t); len(left) > 0 {
		t.Fatalf("processes of this install outlived sisf down: %v", left)
	}
}

// edgeRevision reads the revision the application reports through the edge.
func edgeRevision(d *standaloneDeployment) (string, error) {
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(d.edgeURL() + "/healthz")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var h struct{ Revision string }
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err := json.Unmarshal(raw, &h); err != nil || resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the application's health answered %d: %s", resp.StatusCode, raw)
	}
	return h.Revision, nil
}

// restartSupervisor stops the supervisor after a release, once by asking
// it to and once by killing it, and each time waits until `sisf up` has
// started it again: the restarted supervisor must come up on the records
// its earlier runs left, and the released application must be served under
// the same revision as before.
func (d *standaloneDeployment) restartSupervisor(t *testing.T) {
	t.Helper()
	before, err := edgeRevision(d)
	if err != nil || before == "" {
		t.Fatalf("the edge serves no revision before the restart: %q, %v", before, err)
	}
	prefix := filepath.Join(d.home, "bin", "sisf-supervisor") + " "
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		pids := d.childPIDs(t, prefix)
		if len(pids) != 1 {
			t.Fatalf("sisf up runs %d supervisors: %v", len(pids), pids)
		}
		old := pids[0]
		if err := syscall.Kill(old, sig); err != nil {
			t.Fatalf("send %v to the supervisor: %v", sig, err)
		}
		eventually(t, fmt.Sprintf("sisf up to start the supervisor again after %v and keep it up", sig), func() (bool, string) {
			now := d.childPIDs(t, prefix)
			if len(now) != 1 || now[0] == old {
				return false, fmt.Sprintf("supervisor processes: %v", now)
			}
			if err := dial(standaloneSupervisor); err != nil {
				return false, err.Error()
			}
			after, err := edgeRevision(d)
			return err == nil && after == before, fmt.Sprintf("the edge serves %q (%v); want %q", after, err, before)
		})
	}
}
