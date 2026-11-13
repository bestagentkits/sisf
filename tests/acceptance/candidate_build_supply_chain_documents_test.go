package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state"
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

// digestAndImage is the report of a build command that speaks the protocol:
// it names its image and digest, and no SBOM or provenance.
func digestAndImage(commit string) map[string]string {
	digest := string(commitDigest(commit))
	return map[string]string{"image-digest": digest, "image": "local/app@" + digest}
}

// stripBuildDocuments replaces the SBOM and provenance of every recorded
// build with references that name no document: what a record would hold
// were a build's claims taken as its supply-chain evidence.
func stripBuildDocuments(t *testing.T, f *harness.Factory) {
	t.Helper()
	err := f.ControlStore.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		entries, err := tx.ListKV(ctx, handoff.BuildNamespace, "build/", 0)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			var rec handoff.BuildRecord
			if json.Unmarshal(entry.Value, &rec) != nil || rec.Commit == "" || entry.Key != handoff.BuildKey(rec.Commit) {
				continue
			}
			rec.SBOM, rec.Provenance = nil, nil
			rec.SBOMRef, rec.ProvenanceRef = "sbom:"+string(rec.Commit), "provenance:"+string(rec.Commit)
			if err := handoff.PutDoc(ctx, tx, handoff.BuildNamespace, entry.Key, rec); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A build whose SBOM and provenance references name no document the
// supervisor made fails the observed-build hard gate on every attempt,
// however well the judge scores it: a high score never buys past a hard
// gate. With no attempt left the verdict is held, and the issue stays in
// QAEvaluation.
func TestABuildWhoseSupplyChainRefsNameNoDocumentFailsTheObservedBuildGateWhateverTheScore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	attempts := f.Manifest.Limits.MaxAttempts
	for range attempts {
		f.ScriptPatch(number, 0, greetingDiff)
	}
	for range 4 * attempts {
		f.Pass()
		f.Tick()
		if res := f.Exporter.Pass(ctx); len(res.Errors) > 0 {
			t.Fatalf("exporter: %v", res.Errors)
		}
		if res := f.Stages.Build.Pass(ctx); len(res.Errors) > 0 {
			t.Fatalf("build stage: %v", res.Errors)
		}
		stripBuildDocuments(t, f)
		if res := f.Stages.QA.Pass(ctx); len(res.Errors) > 0 {
			t.Fatalf("QA stage: %v", res.Errors)
		}
		f.Pass()
	}
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

// A sandboxed build is released on the SBOM and provenance the supervisor
// made of the program it received: the record holds both documents, each
// reference is the digest of its document, and modules the supervisor could
// not read from the program are not listed as known.
func TestASandboxedBuildIsReleasedOnTheDocumentsTheSupervisorMade(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	f.Stages.Build.Builder.Images = stages.SandboxImages{
		Exec: &reportingSandbox{report: digestAndImage}, Argv: []string{"build"}, Timeout: time.Minute, Programs: programStore(t),
	}

	f.Cycle(6)
	wantSurfaces(t, f, number, core.StateClosed)
	r := qaResult(t, f, number, 1)
	if r.Verdict != stages.VerdictPassed {
		t.Fatalf("the first attempt was %s: %s", r.Verdict, r.Reason)
	}
	var rec handoff.BuildRecord
	if err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		rec, err = handoff.ReadBuild(ctx, tx, r.Candidate.CommitSHA)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if string(commitDigest(string(rec.Commit))) != string(rec.Digest) || rec.SBOMRef != sha256Text(rec.SBOM) || rec.ProvenanceRef != sha256Text(rec.Provenance) {
		t.Fatalf("the build record's references are not the digests of its documents: %s %s", rec.SBOMRef, rec.ProvenanceRef)
	}
	if !strings.Contains(string(rec.SBOM), "unknown: the program carries no Go build information") {
		t.Fatalf("the SBOM of a program without build information does not say so:\n%s", rec.SBOM)
	}
}

// A sandboxed build command whose report still names an SBOM and a
// provenance does not report as the build protocol asks: what it names is
// never taken as evidence, and the build is held for the Owner as an outage
// of the build command, spending no attempt, rather than charged to the
// patch.
func TestABuildCommandThatClaimsAnSBOMAndProvenanceIsHeldNotJudged(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	exportedPatch(t, f, number)
	build := f.Stages.Build
	build.Builder.Images = stages.SandboxImages{
		Exec: &reportingSandbox{report: func(commit string) map[string]string {
			report := digestAndImage(commit)
			report["sbom"], report["provenance"] = "sbom:"+commit, "provenance:"+commit
			return report
		}}, Argv: []string{"build"}, Timeout: time.Minute, Programs: programStore(t),
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

// sha256Text is the digest of data as a reference names it.
func sha256Text(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
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
