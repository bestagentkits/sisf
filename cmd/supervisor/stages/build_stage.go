package stages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	iqa "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state"
)

// BuildStage turns each patch the agent zone exported into a candidate:
// built on the approved policy commit, committed to the factory's own
// branch, its image built and recorded, and submitted for evaluation under
// the issue lease. A patch the builder refuses (see PatchRefusal) is
// submitted as a failed build, which the evaluator fails without judging,
// so the run's next coding attempt can start within its limit. Any other
// failure, of GitHub, the image builder or the configuration, spends no
// attempt: the patch is tried again on a later pass, after a growing
// backoff, until BuildRetry.HoldAfter outages in a row hold it for the
// Owner.
type BuildStage struct {
	Owner   string
	State   state.Store
	Builder *CandidateBuilder
	// QAPolicy and CodingPolicy read the policy in force, pinned for each step.
	QAPolicy     PolicyRefs
	CodingPolicy PolicyRefs
	Clock        core.Clock
	LeaseTTL     time.Duration
	// Retry bounds how an outage is retried; DefaultBuildRetry when zero.
	Retry BuildRetry
}

// BuildPassResult lists what one pass built and what it could not.
type BuildPassResult struct {
	Submitted, Unbuildable []core.IssueRef
	// Deferred are patches whose last build failed on an outage and whose
	// backoff has not run out. Held are those this pass held, after as
	// many outages in a row as BuildRetry allows, and StillHeld those held
	// earlier. A held build is not tried again until the Owner retries the
	// issue.
	Deferred, Held, StillHeld []core.IssueRef
	Errors                    []error
}

// buildJob is one exported patch waiting for its build.
type buildJob struct {
	record core.StateRecord
	patch  handoff.PatchDoc
	retry  retryEntry
}

// Pass builds the patches waiting in Developing.
func (s *BuildStage) Pass(ctx context.Context) BuildPassResult {
	var res BuildPassResult
	var jobs []buildJob
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
			if _, cerr := handoff.ReadCandidate(ctx, tx, r.RunID, r.Attempt); !core.HasCode(cerr, core.CodeNotFound) {
				continue
			}
			retry, rerr := readRetry(ctx, tx, r.RunID, r.Attempt)
			if rerr != nil {
				res.Errors = append(res.Errors, fmt.Errorf("%s: %w", r.IssueID, rerr))
				continue
			}
			jobs = append(jobs, buildJob{record: r, patch: patch, retry: retry})
		}
		return err
	})
	if err != nil {
		return BuildPassResult{Errors: []error{err}}
	}
	retry := s.Retry.settled()
	now := s.Clock.Now()
	for _, j := range jobs {
		// A held build is not tried again until the Owner's retry resumes
		// it: outages never spend compute without bound.
		if j.retry.Held {
			res.StillHeld = append(res.StillHeld, j.record.IssueID)
			continue
		}
		if j.retry.waiting(now) {
			res.Deferred = append(res.Deferred, j.record.IssueID)
			continue
		}
		failed, err := s.build(ctx, j)
		switch {
		case core.HasCode(err, core.CodeLeaseHeld):
		case err != nil:
			res.Errors = append(res.Errors, fmt.Errorf("%s: %w", j.record.IssueID, err))
			next, rerr := s.recordOutage(ctx, j, err, retry)
			if rerr != nil {
				res.Errors = append(res.Errors, fmt.Errorf("%s: %w", j.record.IssueID, rerr))
			}
			if next.Held {
				res.Held = append(res.Held, j.record.IssueID)
			}
		case failed:
			res.Unbuildable = append(res.Unbuildable, j.record.IssueID)
		default:
			res.Submitted = append(res.Submitted, j.record.IssueID)
		}
	}
	return res
}

// build builds one patch and submits the outcome. It returns an error, and
// submits nothing, for every failure that is not a refusal of the patch.
func (s *BuildStage) build(ctx context.Context, j buildJob) (failed bool, err error) {
	record, patch := j.record, j.patch
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
	coder, err := s.trustedCoder(ctx, record, patch)
	if err != nil {
		return false, err
	}
	var built Built
	var buildErr error
	if patch.Attempt != record.Attempt || patch.RunID != record.RunID {
		buildErr = refuse(core.CodeTampered, RefusalAttemptMismatch, "", "the patch document names another run or attempt than the run's record")
	} else {
		// The attempt in the branch name is the state record's, never the
		// agent zone's: a patch cannot pick the branch it is pushed to.
		built, buildErr = s.Builder.Build(ctx, BuildRequest{
			Issue: record.IssueID, RunID: record.RunID, Attempt: record.Attempt, Base: qaRefs.PolicySHA, Diff: patch.Diff,
		})
	}
	refusal, refused := RefusalOf(buildErr)
	if buildErr != nil && !refused {
		return false, buildErr
	}
	doc := handoff.CandidateDoc{
		Candidate: qa.Candidate{
			IssueID: record.IssueID, RunID: record.RunID, BaselineSHA: qaRefs.PolicySHA, SuiteDigest: handoff.SuiteDigest(qaRefs),
			Policy: qaRefs, CodingAgentIdentity: coder, CodingModelRoute: coding.ModelRoute,
			WorkspaceID: string(record.RunID), Attempt: record.Attempt,
		},
		AgentAttempt: patch.AgentAttempt, Summary: patch.Summary, SubmittedAt: s.Clock.Now().UTC(),
	}
	digest := diffDigest(patch.Diff)
	if refused {
		doc.BuildFailure, doc.BuildRefusal = refusal.Error(), string(refusal.Reason)
		// A build command that failed ran on a commit: keep where, as evidence.
		doc.Candidate.CommitSHA, doc.Candidate.ChangedPaths, doc.Unit, doc.Branch =
			built.Record.Commit, built.ChangedPaths, built.Record.Unit, built.Branch
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
	if refused {
		t.Reason = "the patch was refused: " + string(refusal.Reason)
	}
	return refused, s.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		if err := tx.CheckFencing(ctx, lease.Resource, lease.Token); err != nil {
			return err
		}
		if !refused {
			if err := handoff.PutDoc(ctx, tx, handoff.BuildNamespace, handoff.BuildKey(built.Record.Commit), built.Record); err != nil {
				return err
			}
		}
		if err := handoff.PutDoc(ctx, tx, handoff.BuildNamespace, t.IdempotencyKey, doc); err != nil {
			return err
		}
		if err := clearRetry(ctx, tx, record.RunID, record.Attempt); err != nil {
			return err
		}
		if err := statemachine.New(s.Clock, statemachine.Guards{}).Check(ctx, t); err != nil {
			return err
		}
		_, err := tx.Transition(ctx, t)
		return err
	})
}

// trustedCoder names the run's coder from the records the store bound to
// the run, or marks it unverified; the patch document's own claim is never
// read.
func (s *BuildStage) trustedCoder(ctx context.Context, record core.StateRecord, patch handoff.PatchDoc) (string, error) {
	identity, verified := "", false
	err := s.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		identity, verified, err = handoff.TrustedCoderIdentity(ctx, tx, record, patch.AgentAttempt)
		return err
	})
	if err != nil {
		return "", err
	}
	if !verified {
		return iqa.UnverifiedCodingAgent, nil
	}
	return identity, nil
}

// diffDigest names a patch that has no build.
func diffDigest(diff string) core.Digest {
	sum := sha256.Sum256([]byte(diff))
	return core.Digest("sha256:" + hex.EncodeToString(sum[:]))
}
