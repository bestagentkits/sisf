package install

import (
	"slices"
	"testing"
)

// A program started with only its own environment keeps the caller's
// TMPDIR, so it can run on a read-only root filesystem, and receives no
// other variable of the caller.
func TestAProgramWithOnlyItsEnvironmentKeepsTheCallersTMPDIR(t *testing.T) {
	t.Parallel()
	caller := map[string]string{"PATH": "/bin", "HOME": "/home/f", "TMPDIR": "/data/tmp", "GITHUB_TOKEN": "secret"}
	got := minimalEnv(func(k string) string { return caller[k] }, []string{"SISF_X=1"})
	want := []string{"PATH=/bin", "HOME=/home/f", "TMPDIR=/data/tmp", "SISF_X=1"}
	if !slices.Equal(got, want) {
		t.Fatalf("env = %v, want %v", got, want)
	}
	delete(caller, "TMPDIR")
	if got := minimalEnv(func(k string) string { return caller[k] }, nil); !slices.Equal(got, []string{"PATH=/bin", "HOME=/home/f"}) {
		t.Fatalf("without TMPDIR env = %v", got)
	}
}
