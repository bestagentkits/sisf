package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"strings"
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
// command passes its test and build phases and, in its report phase,
// reports under the nonce it was given the keys report returns for the
// commit it was told, and exports the commit's program: the commit's own
// bytes, whose digest commitDigest is.
type reportingSandbox struct {
	buildSandbox
	report func(commit string) map[string]string
}

func (s *reportingSandbox) Exec(_ context.Context, _ execution.SandboxID, cmd execution.Command) (execution.ExecResult, error) {
	if cmd.Env["SISF_PHASE"] != "report" {
		return execution.ExecResult{}, nil
	}
	var out strings.Builder
	commit := cmd.Env["SISF_REPORT_COMMIT"]
	for key, value := range s.report(commit) {
		fmt.Fprintf(&out, "sisf-report %s %s: %s\n", cmd.Env["SISF_REPORT_NONCE"], key, value)
	}
	fmt.Fprintf(&out, "sisf-report %s program: %s\n", cmd.Env["SISF_REPORT_NONCE"], base64.StdEncoding.EncodeToString([]byte(commit)))
	if cmd.Stdout == nil {
		return execution.ExecResult{Stdout: []byte(out.String())}, nil
	}
	_, err := io.WriteString(cmd.Stdout, out.String())
	return execution.ExecResult{}, err
}

// commitDigest is the image digest the fake builds report for a commit.
func commitDigest(commit string) core.Digest {
	sum := sha256.Sum256([]byte(commit))
	return core.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

// digestAndImage is the report of a build command that names its image and
// digest but no SBOM and no provenance.
func digestAndImage(commit string) map[string]string {
	digest := string(commitDigest(commit))
	return map[string]string{"image-digest": digest, "image": "local/app@" + digest}
}

// unrecordedImages builds images with tests passed but names no SBOM and no
// provenance: an image builder other than the sandbox, whose results the
// observed-build hard gate alone stands between and a release.
type unrecordedImages struct{}

func (unrecordedImages) Name() string { return "unrecorded" }

func (unrecordedImages) BuildImage(_ context.Context, req stages.ImageRequest) (stages.ImageResult, error) {
	digest := commitDigest(string(req.Commit))
	return stages.ImageResult{Digest: digest, Image: "local/app@" + string(digest), TestsPassed: true, TestEvidence: "passed"}, nil
}

// A build that names no SBOM and no provenance fails the observed-build
// hard gate on every attempt, however well the judge scores it: a high
// score never buys past a hard gate. With no attempt left the verdict is
// held, and the issue stays in QAEvaluation.
func TestABuildWithoutSBOMAndProvenanceFailsTheObservedBuildGateWhateverTheScore(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	attempts := f.Manifest.Limits.MaxAttempts
	for range attempts {
		f.ScriptPatch(number, 0, greetingDiff)
	}
	f.Stages.Build.Builder.Images = unrecordedImages{}

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
		Exec: &reportingSandbox{report: func(commit string) map[string]string {
			report := digestAndImage(commit)
			report["sbom"], report["provenance"] = "sbom:"+commit, "provenance:"+commit
			return report
		}}, Argv: []string{"build"}, Timeout: time.Minute, Programs: programStore(t),
	}

	f.Cycle(6)
	wantSurfaces(t, f, number, core.StateClosed)
	if r := qaResult(t, f, number, 1); r.Verdict != stages.VerdictPassed {
		t.Fatalf("the first attempt was %s: %s", r.Verdict, r.Reason)
	}
}

// A sandboxed build command whose report names no SBOM and no provenance
// does not report as the build protocol asks: its candidate is never judged
// on it, and the build is held for the Owner as an outage of the build
// command, spending no attempt, rather than charged to the patch.
func TestASandboxedBuildReportingNoSBOMOrProvenanceIsHeldNotJudged(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	exportedPatch(t, f, number)
	build := f.Stages.Build
	build.Builder.Images = stages.SandboxImages{
		Exec: &reportingSandbox{report: digestAndImage}, Argv: []string{"build"}, Timeout: time.Minute, Programs: programStore(t),
	}
	build.Retry = stages.BuildRetry{First: time.Minute, Max: time.Minute, HoldAfter: 1, RemindEvery: 24 * time.Hour, MaxReminders: 1}

	res := build.Pass(context.Background())
	if !slices.Equal(res.Held, []core.IssueRef{f.Issue(number)}) || len(res.Unbuildable) != 0 || len(res.Submitted) != 0 {
		t.Fatalf("the build was not held as an outage: %+v", res)
	}
	record := f.Record(number)
	if record.State != core.StateDeveloping || record.Attempt != 1 {
		t.Fatalf("the issue is %s at attempt %d, want Developing at its first attempt", record.State, record.Attempt)
	}
	if held := heldListing(t, f); len(held) != 1 || held[0].RunID != record.RunID {
		t.Fatalf("the build was not held for the Owner: %+v", held)
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
