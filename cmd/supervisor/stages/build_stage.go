package stages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state"
)

// BuildStage turns each patch the agent zone exported into a candidate:
// built on the approved policy commit, committed to the factory's own
// branch, its image built and recorded, and submitted for evaluation under
// the issue lease. A patch that cannot be built is submitted as a failed
// build, which the evaluator fails without judging, so the run's next
// coding attempt can start within its limit.
type BuildStage struct {
	Owner   string
	State   state.Store
	Builder *CandidateBuilder
	// QAPolicy and CodingPolicy read the policy in force, pinned for each step.
	QAPolicy     PolicyRefs
	CodingPolicy PolicyRefs
	Clock        core.Clock
	LeaseTTL     time.Duration
}

// BuildPassResult lists what one pass built and what it could not.
type BuildPassResult struct {
	Submitted, Unbuildable []core.IssueRef
	Errors                 []error
}

// Pass builds the patches waiting in Developing.
func (s *BuildStage) Pass(ctx context.Context) BuildPassResult {
	var res BuildPassResult
	type job struct {
		record core.StateRecord
		patch  handoff.PatchDoc
	}
	var jobs []job
	err := s.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		records, err := tx.ListRecords(ctx, state.RecordFilter{States: []core.State{core.StateDeveloping}})
		for _, r := range records {
			patch, perr := handoff.ReadPatch(ctx, tx, r.RunID, r.Attempt)
			if perr != nil {
				if !core.HasCode(perr, core.CodeNotFound) {
					res.Errors = append(res.Errors, fmt.Errorf("%s: %w", r.IssueID, perr))
				}
				continue
			}
			if _, cerr := handoff.ReadCandidate(ctx, tx, r.RunID, r.Attempt); core.HasCode(cerr, core.CodeNotFound) {
				jobs = append(jobs, job{record: r, patch: patch})
			}
		}
		return err
	})
	if err != nil {
		return BuildPassResult{Errors: []error{err}}
	}
	for _, j := range jobs {
		failed, err := s.build(ctx, j.record, j.patch)
		switch {
		case err != nil && !core.HasCode(err, core.CodeLeaseHeld):
			res.Errors = append(res.Errors, fmt.Errorf("%s: %w", j.record.IssueID, err))
		case err != nil:
		case failed:
			res.Unbuildable = append(res.Unbuildable, j.record.IssueID)
		default:
			res.Submitted = append(res.Submitted, j.record.IssueID)
		}
	}
	return res
}

// build builds one patch and submits the outcome.
func (s *BuildStage) build(ctx context.Context, record core.StateRecord, patch handoff.PatchDoc) (failed bool, err error) {
	qaRefs, err := s.QAPolicy(ctx)
	if err != nil {
		return false, err
	}
	coding, err := s.CodingPolicy(ctx)
	if err != nil {
		return false, err
	}
	lease, err := takeLease(ctx, s.State, record.IssueID, s.Owner, s.LeaseTTL)
	if err != nil {
		return false, err
	}
	defer letGo(ctx, s.State, lease)
	built, buildErr := s.Builder.Build(ctx, BuildRequest{
		Issue: record.IssueID, RunID: record.RunID, Attempt: patch.AgentAttempt, Base: qaRefs.PolicySHA, Diff: patch.Diff,
	})
	if buildErr != nil && !unbuildable(buildErr) {
		return false, buildErr
	}
	doc := handoff.CandidateDoc{
		Candidate: qa.Candidate{
			IssueID: record.IssueID, RunID: record.RunID, BaselineSHA: qaRefs.PolicySHA, SuiteDigest: handoff.SuiteDigest(qaRefs),
			Policy: qaRefs, CodingAgentIdentity: patch.CoderIdentity, CodingModelRoute: coding.ModelRoute,
			WorkspaceID: string(record.RunID), Attempt: record.Attempt,
		},
		AgentAttempt: patch.AgentAttempt, Summary: patch.Summary, SubmittedAt: s.Clock.Now().UTC(),
	}
	digest := diffDigest(patch.Diff)
	if buildErr != nil {
		doc.BuildFailure = string(core.CodeOf(buildErr)) + ": " + buildErr.Error()
	} else {
		doc.Candidate.CommitSHA, doc.Candidate.Digest, doc.Candidate.ChangedPaths = built.Record.Commit, built.Record.Digest, built.ChangedPaths
		doc.Unit, doc.Image, doc.Branch, digest = built.Record.Unit, built.Record.Image, built.Branch, built.Record.Digest
	}
	t := core.Transition{
		IssueID: record.IssueID, From: core.StateDeveloping, To: core.StateQAEvaluation, Event: core.EventCandidateSubmitted,
		ExpectedVersion: record.Version, IdempotencyKey: handoff.CandidateKey(record.RunID, record.Attempt),
		LeaseOwner: lease.Owner, FencingToken: lease.Token, RunID: record.RunID, Attempt: record.Attempt,
		CandidateDigest: digest, ActorID: s.Owner, Reason: "built on " + doc.Branch,
	}
	if buildErr != nil {
		t.Reason = "the patch could not be built"
	}
	return buildErr != nil, s.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		if err := tx.CheckFencing(ctx, lease.Resource, lease.Token); err != nil {
			return err
		}
		if buildErr == nil {
			if err := handoff.PutDoc(ctx, tx, handoff.BuildNamespace, handoff.BuildKey(built.Record.Commit), built.Record); err != nil {
				return err
			}
		}
		if err := handoff.PutDoc(ctx, tx, handoff.BuildNamespace, t.IdempotencyKey, doc); err != nil {
			return err
		}
		if err := statemachine.New(s.Clock, statemachine.Guards{}).Check(ctx, t); err != nil {
			return err
		}
		_, err := tx.Transition(ctx, t)
		return err
	})
}

// diffDigest names a patch that has no build.
func diffDigest(diff string) core.Digest {
	sum := sha256.Sum256([]byte(diff))
	return core.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

// unbuildable reports a build failure that lies in the patch itself.
func unbuildable(err error) bool {
	for _, code := range []core.ErrorCode{core.CodeInvalidArgument, core.CodeConflict, core.CodeTampered, core.CodeNotFound} {
		if core.HasCode(err, code) {
			return true
		}
	}
	return false
}
