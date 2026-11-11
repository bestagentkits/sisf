package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// reportingSandbox stands in for an isolating execution backend whose build
// command succeeds and reports, for the commit it was given, exactly the
// lines report returns.
type reportingSandbox struct {
	buildSandbox
	report func(commit string) string
}

func (s *reportingSandbox) Exec(_ context.Context, _ execution.SandboxID, cmd execution.Command) (execution.ExecResult, error) {
	return execution.ExecResult{Stdout: []byte(s.report(cmd.Env["SISF_COMMIT"]))}, nil
}

// testsDigestAndImage is what a build command prints when it reports its
// tests, its image and its digest but no SBOM and no provenance.
func testsDigestAndImage(commit string) string {
	sum := sha256.Sum256([]byte(commit))
	digest := "sha256:" + hex.EncodeToString(sum[:])
	return "sisf-tests: passed\nsisf-image-digest: " + digest + "\nsisf-image: local/app@" + digest + "\n"
}

// A sandboxed build that reports no SBOM and no provenance fails the
// observed-build hard gate on every attempt, however well the judge
// scores it: a high score never buys past a hard gate. With no attempt
// left the verdict is held, and the issue stays in QAEvaluation.
func TestABuildWithoutSBOMAndProvenanceFailsTheObservedBuildGateWhateverTheScore(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	attempts := f.Manifest.Limits.MaxAttempts
	for range attempts {
		f.ScriptPatch(number, 0, greetingDiff)
	}
	f.Stages.Build.Builder.Images = stages.SandboxImages{
		Exec: &reportingSandbox{report: testsDigestAndImage}, Argv: []string{"build"}, Timeout: time.Minute,
	}

	f.Cycle(4 * attempts)
	record := f.Record(number)
	if record.State != core.StateQAEvaluation || record.Attempt != attempts {
		t.Fatalf("the issue is %s at attempt %d, want QAEvaluation at %d", record.State, record.Attempt, attempts)
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		r := qaResult(t, f, number, attempt)
		want := stages.VerdictFailed
		if attempt == attempts {
			want = stages.VerdictHeld
		}
		if r.Verdict != want || r.Receipt.Score != 100 || !failedCheck(r.Receipt, string(qa.GateObservedBuild)) {
			t.Fatalf("attempt %d: verdict %s, score %d, reason %q; want %s at 100 with the observed-build gate failed",
				attempt, r.Verdict, r.Receipt.Score, r.Reason, want)
		}
	}
}

// The same build, reporting where its SBOM and provenance are, passes
// every hard gate and is released and closed on its first attempt.
func TestABuildThatReportsItsSBOMAndProvenanceIsReleased(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	f.Stages.Build.Builder.Images = stages.SandboxImages{
		Exec: &reportingSandbox{report: func(commit string) string {
			return testsDigestAndImage(commit) + "sisf-sbom: sbom:" + commit + "\nsisf-provenance: provenance:" + commit + "\n"
		}}, Argv: []string{"build"}, Timeout: time.Minute,
	}

	f.Cycle(6)
	wantSurfaces(t, f, number, core.StateClosed)
	if r := qaResult(t, f, number, 1); r.Verdict != stages.VerdictPassed {
		t.Fatalf("the first attempt was %s: %s", r.Verdict, r.Reason)
	}
}

// failedCheck reports whether the receipt records the named hard gate as failed.
func failedCheck(receipt core.QAReceipt, gate string) bool {
	for _, c := range receipt.Checks {
		if c.Kind == core.CheckHardGate && c.Name == gate {
			return !c.Passed
		}
	}
	return false
}
