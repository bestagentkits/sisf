package release

import (
	"context"
	"time"

	iqa "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/internal/supplychain"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// Request asks the supervisor to release the candidate of one run. Its
// content is untrusted until verified: the receipt and the artifact carry
// signatures the supervisor checks itself.
type Request struct {
	IssueID core.IssueRef `json:"issue_id"`
	RunID   core.RunID    `json:"run_id"`
	// Candidate is the candidate as QA judged it, its changed paths included.
	Candidate qa.Candidate   `json:"candidate"`
	Receipt   core.QAReceipt `json:"receipt"`
	// Artifact is the signed, immutable build of the candidate.
	Artifact deploy.Artifact `json:"artifact"`
	// Feature is the approved catalog entry the release delivers, if any.
	Feature string `json:"feature,omitempty"`
	// Summary is the changelog line of the release.
	Summary string `json:"summary,omitempty"`
}

// validate requires the request to name one issue and run throughout.
func (r Request) validate() error {
	if err := r.IssueID.Validate(); err != nil {
		return err
	}
	if err := r.RunID.Validate(); err != nil {
		return err
	}
	if r.Candidate.IssueID != r.IssueID || r.Candidate.RunID != r.RunID {
		return invalid("the candidate belongs to another issue or run than the release")
	}
	return nil
}

// verifiedRelease is a release whose receipt and artifact passed
// verification. Only verify builds one, so a value of this type is proof.
type verifiedRelease struct {
	issue      core.IssueRef
	run        core.RunID
	artifact   deploy.Artifact
	receipt    core.QAReceipt
	verifiedAt time.Time
	// sequence is the place of the release's Verified step in the order
	// of Verified steps, once it was written: the LKG keeps it to order
	// releases by their verification.
	sequence int64
}

// verify accepts a release only when all of the following hold:
//
//   - the receipt is complete, signed by a trusted evaluator key, unexpired,
//     and bound to this factory and to the candidate the record names;
//   - the receipt passes: every required hard gate passed and the score
//     reaches the pass score;
//   - the artifact is the candidate QA judged, a pinned build of this
//     factory with SBOM and provenance, signed by a trusted artifact key.
func (p *Pipeline) verify(ctx context.Context, req Request, record core.StateRecord) (verifiedRelease, error) {
	now := p.cfg.Clock.Now()
	if record.CandidateDigest != req.Candidate.Digest {
		return verifiedRelease{}, core.NewError(core.CodeGuardFailed, "the candidate is not the one the issue's run submitted")
	}
	if err := iqa.VerifyReceipt(ctx, p.cfg.Verifier, req.Receipt, p.cfg.FactoryID, req.Candidate, now); err != nil {
		return verifiedRelease{}, err
	}
	if err := req.Receipt.CheckPass(p.cfg.PassScore, p.cfg.ExtraGates...); err != nil {
		return verifiedRelease{}, err
	}
	if req.Artifact.Digest != core.ReleaseDigest(req.Candidate.Digest) {
		return verifiedRelease{}, core.NewError(core.CodeGuardFailed, "the artifact is another build than the candidate QA judged")
	}
	if err := supplychain.VerifyArtifact(ctx, p.cfg.Verifier, req.Artifact, p.cfg.FactoryID, req.Artifact.Digest); err != nil {
		return verifiedRelease{}, err
	}
	return verifiedRelease{issue: req.IssueID, run: req.RunID, artifact: req.Artifact, receipt: req.Receipt, verifiedAt: now}, nil
}
