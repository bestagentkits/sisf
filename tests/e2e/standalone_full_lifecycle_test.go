//go:build e2e

package e2e

import "testing"

// A Standalone factory, every unit a local process on SQLite, takes one issue
// the whole way: built and judged as local processes, released by canary to
// local application processes behind the edge, verified, published and
// closed.
func TestStandaloneReleasesAnIssueTheWholeWay(t *testing.T) {
	d := newStandaloneDeployment(t)
	runFullLifecycle(t, d)
	d.down(t)
	if left := d.installProcesses(t); len(left) > 0 {
		t.Fatalf("processes of this install outlived sisf down: %v", left)
	}
}
