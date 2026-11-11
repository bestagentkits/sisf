package stages

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/internal/supplychain"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Releaser runs a candidate through the release pipeline, and undoes a
// release left with nothing watching it.
type Releaser interface {
	Release(ctx context.Context, req release.Request) (release.Outcome, error)
	Recover(ctx context.Context, issue core.IssueRef, unit string) (release.Outcome, error)
}

// ReleaseStage hands every release candidate, with its signed receipt and
// its signed artifact, to the release pipeline once. The pipeline decides
// everything after: it holds, refuses, rolls back or closes. It also has
// the pipeline undo every release whose rollout started and that nothing
// watches any more (Recover).
type ReleaseStage struct {
	FactoryID core.FactoryID
	State     state.Store
	Pipeline  Releaser
	// ArtifactSigner is the supervisor's delegated deploy_artifact key.
	ArtifactSigner core.Signer
	Clock          core.Clock
	// Waits is optional; with it what a verified release waits on is
	// recorded on its candidate's pull request.
	Waits FinishWaits

	// mu guards reported: what each verified release was last reported
	// to wait on, so a wait is reported once per change.
	mu       sync.Mutex
	reported map[core.IssueRef]string
}

// ReleasePassResult lists the releases one pass ran, by result.
type ReleasePassResult struct {
	Outcomes map[core.IssueRef]release.Outcome
	Errors   []error
}

// Pass first undoes the releases left unwatched (Recover), then releases
// the candidates waiting in ReleaseCandidate that have no release yet, or
// whose held release the Owner resumed, then finishes the verified ones
// whose landing is pending.
func (s *ReleaseStage) Pass(ctx context.Context) ReleasePassResult {
	res := s.Recover(ctx)
	type job struct {
		record core.StateRecord
		qa     QAResult
		build  handoff.BuildRecord
		doc    handoff.CandidateDoc
	}
	var jobs []job
	err := s.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		records, err := tx.ListRecords(ctx, state.RecordFilter{States: []core.State{core.StateReleaseCandidate}})
		if err != nil {
			return err
		}
		for _, r := range records {
			if again, err := releaseDue(ctx, tx, r); err != nil {
				res.Errors = append(res.Errors, fmt.Errorf("%s: %w", r.IssueID, err))
				continue
			} else if !again {
				continue
			}
			j := job{record: r}
			if j.qa, j.doc, j.build, err = readRequestRecords(ctx, tx, r); err != nil {
				res.Errors = append(res.Errors, fmt.Errorf("%s: %w", r.IssueID, err))
				continue
			}
			jobs = append(jobs, j)
		}
		return nil
	})
	if err != nil {
		res.Errors = append(res.Errors, err)
		return res
	}
	for _, j := range jobs {
		out, err := s.release(ctx, j.record, j.qa, j.doc, j.build)
		if err != nil && out.Result == "" {
			res.Errors = append(res.Errors, fmt.Errorf("%s: %w", j.record.IssueID, err))
			continue
		}
		res.Outcomes[j.record.IssueID] = out
		if out.State == core.StateVerified && out.Waits != "" {
			s.reportWait(ctx, j.record, out.Waits, &res)
		}
	}
	s.resumeVerified(ctx, &res)
	return res
}

// releaseDue reports whether the candidate of r is to be released: it has
// no release yet, the Owner resumed its held release, or an earlier release
// recorded a refusal without blocking the issue, which is judged again.
func releaseDue(ctx context.Context, tx state.Tx, r core.StateRecord) (bool, error) {
	_, err := ReadRelease(ctx, tx, r.RunID, r.Attempt)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return true, nil
	case err != nil:
		return false, err
	}
	hold, err := handoff.ReadReleaseHold(ctx, tx, r.RunID, r.Attempt)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	return hold.Resumed(), nil
}

// release signs the build as a deploy artifact and runs the pipeline. The
// outcome is recorded, so a held release is not retried by itself: it is
// also recorded as the release's hold, which the Owner sees, and only the
// Owner's retry lets it start over. A hold is written only for a release
// that left its issue in ReleaseCandidate with nothing deployed; a refusal
// whose block could not be written is recorded nowhere, so the next pass
// judges it again.
func (s *ReleaseStage) release(ctx context.Context, record core.StateRecord, r QAResult, doc handoff.CandidateDoc, build handoff.BuildRecord) (release.Outcome, error) {
	req, err := s.request(ctx, record, r, doc, build)
	if err != nil {
		return release.Outcome{}, err
	}
	out, relErr := s.Pipeline.Release(ctx, req)
	if out.Result == "" || (out.Result == release.ResultRefused && out.State == core.StateReleaseCandidate) {
		return out, relErr
	}
	return out, errors.Join(relErr, s.recordOutcome(ctx, record, out, relErr))
}

// request is the release request of record's candidate: the build signed
// as a deploy artifact, with the candidate and the receipt QA judged and
// the changelog line of the run.
func (s *ReleaseStage) request(ctx context.Context, record core.StateRecord, r QAResult, doc handoff.CandidateDoc, build handoff.BuildRecord) (release.Request, error) {
	artifact, err := supplychain.SignArtifact(ctx, s.ArtifactSigner, deploy.Artifact{
		FactoryID: s.FactoryID, Unit: build.Unit, Digest: core.ReleaseDigest(build.Digest), Image: build.Image,
		SBOMRef: build.SBOMRef, ProvenanceRef: build.ProvenanceRef,
	})
	if err != nil {
		return release.Request{}, err
	}
	return release.Request{
		IssueID: record.IssueID, RunID: record.RunID, Candidate: r.Candidate, Receipt: r.Receipt,
		Artifact: artifact, Feature: doc.Feature, Summary: doc.Summary,
	}, nil
}

// readRequestRecords reads what the release request of r's candidate is
// rebuilt from: the QA result, the candidate document and the build.
func readRequestRecords(ctx context.Context, tx state.Tx, r core.StateRecord) (QAResult, handoff.CandidateDoc, handoff.BuildRecord, error) {
	q, err := ReadQAResult(ctx, tx, r.RunID, r.Attempt)
	if err != nil {
		return QAResult{}, handoff.CandidateDoc{}, handoff.BuildRecord{}, err
	}
	doc, err := handoff.ReadCandidate(ctx, tx, r.RunID, r.Attempt)
	if err != nil {
		return QAResult{}, handoff.CandidateDoc{}, handoff.BuildRecord{}, err
	}
	build, err := handoff.ReadBuild(ctx, tx, q.Candidate.CommitSHA)
	return q, doc, build, err
}

// recordOutcome keeps how a release of record's candidate ended and, for a
// held one, its hold; any other ending drops the hold.
func (s *ReleaseStage) recordOutcome(ctx context.Context, record core.StateRecord, out release.Outcome, relErr error) error {
	rec := ReleaseRecord{Result: string(out.Result), State: out.State, Release: out.Release, Reason: out.Reason, At: s.Clock.Now().UTC()}
	if relErr != nil {
		rec.Code = string(core.CodeOf(relErr))
	}
	return s.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		if err := put(ctx, tx, ReleaseKey(record.RunID, record.Attempt), rec); err != nil {
			return err
		}
		if out.Result != release.ResultHeld || out.State != core.StateReleaseCandidate {
			return handoff.DropReleaseHold(ctx, tx, record.RunID, record.Attempt)
		}
		return handoff.PutReleaseHold(ctx, tx, handoff.ReleaseHold{
			IssueID: record.IssueID, RunID: record.RunID, Attempt: record.Attempt, Result: string(out.Result),
			Code: core.CodeOf(relErr), Reason: out.Reason, Release: out.Release, HeldAt: rec.At,
		})
	})
}

// Recover has the pipeline undo every release whose rollout started and
// that nothing of this supervisor watches: one a supervisor that stopped
// left in Canary or Monitoring, and one whose restore could not block its
// issue. The LKG of the release's unit is restored and the issue blocked.
// It runs when the supervisor starts and at the start of every pass; a
// release the pipeline drives is left alone. When another holder keeps an
// issue's lease the LKG is restored all the same, the steps are left for a
// pass that can take the lease, and the wait is reported once per change.
func (s *ReleaseStage) Recover(ctx context.Context) ReleasePassResult {
	res := ReleasePassResult{Outcomes: map[core.IssueRef]release.Outcome{}}
	type job struct {
		record core.StateRecord
		unit   string
	}
	var jobs []job
	err := s.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		records, err := tx.ListRecords(ctx, state.RecordFilter{States: release.RecoverableStates()})
		if err != nil {
			return err
		}
		for _, r := range records {
			unit, err := releasedUnit(ctx, tx, r)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Errorf("%s: the unit of its release in %s: %w", r.IssueID, r.State, err))
				continue
			}
			jobs = append(jobs, job{record: r, unit: unit})
		}
		return nil
	})
	if err != nil {
		res.Errors = append(res.Errors, err)
		return res
	}
	for _, j := range jobs {
		out, err := s.Pipeline.Recover(ctx, j.record.IssueID, j.unit)
		switch {
		case core.HasCode(err, core.CodeLeaseHeld):
			// Another holder keeps the issue: the LKG was restored without
			// the lease, and the steps wait for it. Said once per change.
			if out.Waits != "" && s.changedWait(j.record.IssueID, out.Waits) {
				res.Outcomes[j.record.IssueID] = out
			}
			continue
		case out.Result == "":
			if err != nil {
				res.Errors = append(res.Errors, fmt.Errorf("%s: recover its release: %w", j.record.IssueID, err))
			}
			continue
		}
		s.forgetWait(j.record.IssueID)
		if recErr := s.recordOutcome(ctx, j.record, out, err); recErr != nil {
			res.Errors = append(res.Errors, fmt.Errorf("%s: record its recovered release: %w", j.record.IssueID, recErr))
		}
		res.Outcomes[j.record.IssueID] = out
	}
	return res
}

// releasedUnit is the unit the release of r's candidate was built as, read
// from the supervisor's own records of the candidate's evaluation and build.
func releasedUnit(ctx context.Context, tx state.Tx, r core.StateRecord) (string, error) {
	qa, err := ReadQAResult(ctx, tx, r.RunID, r.Attempt)
	if err != nil {
		return "", err
	}
	build, err := handoff.ReadBuild(ctx, tx, qa.Candidate.CommitSHA)
	if err != nil {
		return "", err
	}
	return build.Unit, nil
}
