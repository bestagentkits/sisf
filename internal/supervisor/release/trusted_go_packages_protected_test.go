package release

import (
	"context"
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/internal/supervisor/trustedclosure"
	"github.com/bestagentkits/sisf/pkg/core"
)

// moduleRoot is the repository root, seen from this package.
const moduleRoot = "../../.."

// The protected Go package directories are generated from what the trusted
// binaries link; a package the supervisor starts to link, or stops linking,
// makes the checked-in list stale until it is generated again.
func TestTheProtectedGoPackagesAreTheTrustedBinariesClosure(t *testing.T) {
	t.Parallel()
	packages, err := trustedclosure.Packages(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	want, err := trustedclosure.Dirs(packages)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(trustedGoPackageDirs, want) {
		t.Fatalf("the generated list of trusted Go packages is stale; run go generate ./internal/supervisor/release/ and commit the result\nchecked in: %v\nlinked now: %v",
			trustedGoPackageDirs, want)
	}
}

// Every directory the trusted binaries link is part of the trusted control
// surface: a candidate that changes any file in it is a governance change.
func TestEveryPackageTheSupervisorLinksIsProtected(t *testing.T) {
	t.Parallel()
	for _, dir := range trustedGoPackageDirs {
		for _, changed := range []string{dir + "x.go", dir + "zz_new_file.go"} {
			if err := checkGovernance(DefaultProtectedPaths(), testUnit, []string{changed}); !core.HasCode(err, core.CodeApprovalRequired) {
				t.Errorf("%s: %v, want APPROVAL_REQUIRED", changed, err)
			}
		}
	}
	for _, dir := range []string{"pkg/approvals/", "adapters/exec/docker/", "pkg/github/", "pkg/execution/",
		"internal/budget/", "internal/audit/", "pkg/manifest/", "internal/edge/"} {
		if !slices.Contains(trustedGoPackageDirs, dir) {
			t.Errorf("%s is linked into the supervisor but missing from the generated list", dir)
		}
	}
}

// A candidate that changes only a package the supervisor links is refused
// as a governance change before anything is staged, deployed or merged.
func TestACandidateThatChangesOnlyALinkedPackageIsRefusedForApproval(t *testing.T) {
	t.Parallel()
	for i, changed := range []string{"pkg/approvals/approvals.go", "adapters/exec/docker/docker.go", "pkg/github/client.go"} {
		t.Run(changed, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			lander := &countingLander{}
			w.cfg.Lander = lander
			issue, run := issueRef(60+i), core.RunID("run-linked")
			w.seedReleaseCandidate(issue, run, firstBuild)
			req := w.request(issue, run, firstBuild)
			req.Candidate.ChangedPaths = []string{changed}
			req.Receipt = w.keys.receiptFor(t, req.Candidate, nil)
			out, err := w.pipeline().Release(context.Background(), req)
			requireRefusedUntouched(t, w, issue, out, err, core.CodeApprovalRequired)
			if lander.lands.Load() != 0 {
				t.Fatal("a refused candidate was merged")
			}
		})
	}
}

// An ordinary change of the application is still released and merged.
func TestAnOrdinaryApplicationChangeIsStillReleasedAndMerged(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	lander := &countingLander{}
	w.cfg.Lander = lander
	issue, run := issueRef(64), core.RunID("run-app-change")
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)
	req.Candidate.ChangedPaths = []string{"examples/hello-app/main.go"}
	req.Receipt = w.keys.receiptFor(t, req.Candidate, nil)
	out, err := w.pipeline().Release(context.Background(), req)
	if err != nil || out.Result != ResultClosed || lander.lands.Load() != 1 {
		t.Fatalf("Release = %+v, %v with %d merges; want closed and merged once", out, err, lander.lands.Load())
	}
}
