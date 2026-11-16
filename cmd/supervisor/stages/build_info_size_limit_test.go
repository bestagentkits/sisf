package stages

import (
	"errors"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
)

// The build information of a candidate's program is untrusted in size: one
// larger than the bound is not put into the SBOM, which then says why it
// lists no module.
func TestBuildInformationLargerThanTheBoundIsNotPutIntoTheSBOM(t *testing.T) {
	t.Parallel()
	small := &debug.BuildInfo{Main: debug.Module{Path: "example.test/app"}}
	if info, unread := usableBuildInfo(small, nil); info != small || unread != "" {
		t.Fatalf("small build information = %v, %q", info, unread)
	}
	large := &debug.BuildInfo{Main: debug.Module{Path: "example.test/app"}}
	for i := 0; len(large.String()) <= maxBuildInfoBytes; i++ {
		large.Deps = append(large.Deps, &debug.Module{Path: "example.test/dependency/" + strconv.Itoa(i) + strings.Repeat("x", 200), Version: "v1.0.0"})
	}
	if info, unread := usableBuildInfo(large, nil); info != nil || !strings.Contains(unread, "larger than") {
		t.Fatalf("oversized build information = %d deps, %q; want it left out and said why", len(large.Deps), unread)
	}
	if info, unread := usableBuildInfo(nil, errors.New("not a Go binary")); info != nil || unread == "" {
		t.Fatalf("a program without build information = %v, %q", info, unread)
	}
}
