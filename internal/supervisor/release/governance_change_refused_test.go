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
		"the zones' lifecycle steps":   {"pkg/state/sqlstore/zone_ownership.go"},
		"the zones' table privileges":  {"pkg/state/sqlstore/zone_privileges.go"},
		"the row policies":             {"pkg/state/sqlstore/row_policies.go"},
		"a migration built in Go":      {"pkg/state/sqlstore/migrations.go"},
		"a policy migration built":     {"pkg/state/sqlstore/row_policy_migration.go"},
		"a new file of the store":      {"pkg/state/sqlstore/zz_more_steps.go"},
		"the store contract":           {"pkg/state/state.go"},
		"the synthetic suite":          {"examples/hello-app/synthetic/synthetic.go"},
		"the synthetic suite program":  {"examples/hello-app/cmd/hello-app-synthetic/main.go"},
		"the installer":                {"internal/install/grants.go"},
		"the operator command line":    {"cmd/sisf/cmd_install.go"},
		"a CI workflow":                {".github/workflows/ci.yml"},
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

// A candidate of the application unit that changes which zone may take
// which lifecycle step, or the migrations of the state store, is refused
// before anything is deployed, whatever QA said: the next build of every
// zone binary would carry the change.
func TestTheReleasePipelineRefusesAChangeOfTheStoreRulesBeforeDeployingAnything(t *testing.T) {
	t.Parallel()
	for i, changed := range []string{"pkg/state/sqlstore/zone_ownership.go", "pkg/state/sqlstore/migrations.go"} {
		w := newWorld(t)
		issue, run := issueRef(42+i), core.RunID("run-store-rules")
		w.seedReleaseCandidate(issue, run, firstBuild)
		req := w.request(issue, run, firstBuild)
		req.Candidate.ChangedPaths = []string{"examples/hello-app/main.go", changed}
		req.Receipt = w.keys.receiptFor(t, req.Candidate, nil)
		out, err := w.pipeline().Release(context.Background(), req)
		requireRefusedUntouched(t, w, issue, out, err, core.CodeApprovalRequired)
	}
}

func TestABuildOfTheSupervisorIsNeverReleasedByThePipeline(t *testing.T) {
	t.Parallel()
	if err := checkGovernance(DefaultProtectedPaths(), "Supervisor", []string{"README.md"}); !core.HasCode(err, core.CodeApprovalRequired) {
		t.Fatalf("a supervisor build = %v, want APPROVAL_REQUIRED", err)
	}
}

func TestAnOrdinaryCandidateIsNotAGovernanceChange(t *testing.T) {
	t.Parallel()
	changed := []string{"examples/hello-app/main.go", "internal/control/http_dashboard.go", "tests/acceptance/x_test.go", "internal/qatools/x.go",
		"pkg/statefile/x.go", "examples/hello-app/synthetic_notes.md"}
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
