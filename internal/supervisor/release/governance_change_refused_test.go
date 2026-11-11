package release

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

func TestACandidateThatTouchesTheTrustedControlSurfaceIsAGovernanceChange(t *testing.T) {
	t.Parallel()
	cases := map[string][]string{
		"the supervisor":               {"internal/supervisor/release/monitor.go"},
		"the supervisor binary":        {"cmd/supervisor/main.go"},
		"the signing keys":             {"internal/signing/delegation/delegation.go"},
		"policy enforcement":           {"internal/policy/policy.go"},
		"the factory configuration":    {".sisf/factory.yaml"},
		"the QA baseline":              {"internal/qa/hard_gates.go"},
		"the review criteria":          {"REVIEW.md"},
		"the rollback rules":           {"pkg/deploy/deploy.go"},
		"case does not hide it":        {"Internal/Supervisor/release/monitor.go"},
		"a dot segment does not hide":  {"examples/../internal/qa/receipt.go"},
		"one protected among others":   {"examples/hello-app/main.go", "internal/signing/keys.go"},
		"a path out of the repository": {"../outside.go"},
		"an absolute path":             {"/etc/passwd"},
		"no listed changes":            nil,
	}
	for name, changed := range cases {
		if err := checkGovernance(DefaultProtectedPaths(), testUnit, changed); !core.HasCode(err, core.CodeApprovalRequired) {
			t.Errorf("%s: %v, want APPROVAL_REQUIRED", name, err)
		}
	}
}

func TestTheReleasePipelineRefusesAGovernanceChangeBeforeDeployingAnything(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	issue, run := issueRef(40), core.RunID("run-40")
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)
	req.Candidate.ChangedPaths = []string{"examples/hello-app/main.go", "internal/supervisor/release/monitor.go"}
	req.Receipt = w.keys.receiptFor(t, req.Candidate, nil)
	out, err := w.pipeline().Release(context.Background(), req)
	requireRefusedUntouched(t, w, issue, out, err, core.CodeApprovalRequired)
}

func TestABuildOfTheSupervisorIsNeverReleasedByThePipeline(t *testing.T) {
	t.Parallel()
	if err := checkGovernance(DefaultProtectedPaths(), "Supervisor", []string{"README.md"}); !core.HasCode(err, core.CodeApprovalRequired) {
		t.Fatalf("a supervisor build = %v, want APPROVAL_REQUIRED", err)
	}
}

func TestAnOrdinaryCandidateIsNotAGovernanceChange(t *testing.T) {
	t.Parallel()
	changed := []string{"examples/hello-app/main.go", "internal/control/http_dashboard.go", "tests/acceptance/x_test.go", "internal/qatools/x.go"}
	if err := checkGovernance(DefaultProtectedPaths(), testUnit, changed); err != nil {
		t.Fatalf("an ordinary candidate was refused: %v", err)
	}
}

// A candidate whose changed paths GitHub may have listed only in part
// cannot be shown to leave the trusted surface alone: it is left for the
// Owner like a governance change, before anything is deployed.
func TestACandidateWhoseChangesMayBeListedInPartIsLeftForTheOwner(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	issue, run := issueRef(41), core.RunID("run-41")
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)
	req.Candidate.ChangedPathsIncomplete = true
	req.Receipt = w.keys.receiptFor(t, req.Candidate, nil)
	out, err := w.pipeline().Release(context.Background(), req)
	requireRefusedUntouched(t, w, issue, out, err, core.CodeApprovalRequired)
}
