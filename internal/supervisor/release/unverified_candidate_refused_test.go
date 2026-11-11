package release

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
)

// requireRefusedUntouched fails the test unless the release was refused with
// code, the issue was blocked with the refusal recorded as its cause,
// nothing was deployed and the LKG did not move.
func requireRefusedUntouched(t *testing.T, w *world, issue core.IssueRef, out Outcome, err error, code core.ErrorCode) {
	t.Helper()
	if !core.HasCode(err, code) || out.Result != ResultRefused || out.State != core.StateBlocked {
		t.Fatalf("Release = %s in %s, %v; want refused with %s and blocked", out.Result, out.State, err, code)
	}
	w.requireState(issue, core.StateBlocked)
	record := w.record(issue)
	if b, found := w.block(record.RunID); !found || b.Cause != statemachine.BlockReleaseRefused || b.Code != code || b.IssueID != issue {
		t.Fatalf("the block of the refused release is %+v (found %v), want cause %s with code %s", b, found, statemachine.BlockReleaseRefused, code)
	}
	promotions, rollbacks, staged, _ := w.backend.snapshot()
	if len(promotions)+len(rollbacks)+len(staged) != 0 {
		t.Fatalf("a refused release still acted: staged %v promoted %v rolled back %v", staged, promotions, rollbacks)
	}
	if lkg := w.lkg(); lkg.Digest != lkgDigest || lkg.Source != SourceReleaseManifest {
		t.Fatalf("the LKG moved to %+v on a refused release", lkg)
	}
}

func TestAReleaseWithAnUnacceptableReceiptIsNeverDeployed(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		mutate func(w *world, req *Request)
		code   core.ErrorCode
	}{
		"receipt for another commit": {func(w *world, req *Request) {
			req.Receipt = w.keys.receiptFor(w.t, req.Candidate, func(r *core.QAReceipt) { r.CandidateSHA = otherSHA })
		}, core.CodeGuardFailed},
		"receipt for another build": {func(w *world, req *Request) {
			req.Receipt = w.keys.receiptFor(w.t, req.Candidate, func(r *core.QAReceipt) { r.CandidateDigest = otherBuild })
		}, core.CodeGuardFailed},
		"expired receipt": {func(w *world, req *Request) {
			req.Receipt = w.keys.receiptFor(w.t, req.Candidate, func(r *core.QAReceipt) {
				r.IssuedAt, r.ExpiresAt = startTime.Add(-2*time.Hour), startTime.Add(-time.Hour)
			})
		}, core.CodeExpired},
		"failed hard gate under a high score": {func(w *world, req *Request) {
			req.Receipt = w.keys.receiptFor(w.t, req.Candidate, func(r *core.QAReceipt) { r.Checks[0].Passed = false })
		}, core.CodeGuardFailed},
		"score below the pass score": {func(w *world, req *Request) {
			req.Receipt = w.keys.receiptFor(w.t, req.Candidate, func(r *core.QAReceipt) {
				for i := range r.Checks {
					if r.Checks[i].Kind == core.CheckRubric {
						r.Checks[i].Points = 0
					}
				}
				r.Score = 0
			})
		}, core.CodeGuardFailed},
		"receipt edited after signing": {func(_ *world, req *Request) { req.Receipt.Score = 100 }, core.CodeSignatureInvalid},
		"receipt signed by a key no root of this factory delegated": {func(w *world, req *Request) {
			req.Receipt = newKeyring(w.t, w.clock).receiptFor(w.t, req.Candidate, nil)
		}, core.CodeSignatureInvalid},
		"candidate of another build than the record": {func(w *world, req *Request) {
			req.Candidate.Digest = otherBuild
			req.Receipt = w.keys.receiptFor(w.t, req.Candidate, nil)
			req.Artifact = w.keys.artifactFor(w.t, otherBuild)
		}, core.CodeGuardFailed},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			issue, run := issueRef(30), core.RunID("run-30")
			w.seedReleaseCandidate(issue, run, firstBuild)
			req := w.request(issue, run, firstBuild)
			c.mutate(w, &req)
			out, err := w.pipeline().Release(context.Background(), req)
			requireRefusedUntouched(t, w, issue, out, err, c.code)
		})
	}
}

func TestAnArtifactWhoseSignatureCannotBeVerifiedNeverBecomesTheLKG(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		mutate func(w *world, req *Request)
		code   core.ErrorCode
	}{
		"unsigned artifact":         {func(_ *world, req *Request) { req.Artifact = unsignedArtifact(firstBuild) }, core.CodeSignatureInvalid},
		"artifact of another build": {func(w *world, req *Request) { req.Artifact = w.keys.artifactFor(w.t, otherBuild) }, core.CodeGuardFailed},
		"artifact relabelled after signing": {func(_ *world, req *Request) {
			req.Artifact.SBOMRef = "sbom:forged"
		}, core.CodeSignatureInvalid},
		"artifact signed by a key no root of this factory delegated": {func(w *world, req *Request) {
			req.Artifact = newKeyring(w.t, w.clock).artifactFor(w.t, firstBuild)
		}, core.CodeSignatureInvalid},
		"artifact for another factory": {func(w *world, req *Request) {
			req.Artifact.FactoryID = "other-factory"
		}, core.CodeInvalidArgument},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			issue, run := issueRef(31), core.RunID("run-31")
			w.seedReleaseCandidate(issue, run, firstBuild)
			req := w.request(issue, run, firstBuild)
			c.mutate(w, &req)
			out, err := w.pipeline().Release(context.Background(), req)
			requireRefusedUntouched(t, w, issue, out, err, c.code)
		})
	}
}

func TestTheLKGBookCannotRecordAnUnverifiedRelease(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	unverified := verifiedRelease{artifact: unsignedArtifact(firstBuild)}
	if err := w.book.record(unverified); !core.HasCode(err, core.CodeSignatureInvalid) {
		t.Fatalf("record of an unverified release = %v, want SIGNATURE_INVALID", err)
	}
	if lkg := w.lkg(); lkg.Digest != lkgDigest {
		t.Fatalf("the LKG moved to %s", lkg.Digest)
	}
}
