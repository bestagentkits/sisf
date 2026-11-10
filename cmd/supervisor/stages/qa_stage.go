package stages

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	iqa "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

// QAStage evaluates every submitted candidate once per attempt.
type QAStage struct {
	FactoryID core.FactoryID
	// Owner names the stage in the issue leases it takes.
	Owner string
	// State is the operational store, opened for the control zone.
	State     state.Store
	Evaluator *iqa.Evaluator
	// Tree lists what a candidate changed, read from the repository.
	Tree   Tree
	Policy PolicyRefs
	// MaxAttempts is the run's attempt limit from the policy in force.
	MaxAttempts int
	PassScore   int
	ExtraGates  []string
	Clock       core.Clock
	LeaseTTL    time.Duration
}

// QAPassResult is what one pass of the stage did, per verdict.
type QAPassResult struct {
	Passed, Failed, Held []core.IssueRef
	Errors               []error
}

// Pass evaluates the candidates waiting in QAEvaluation.
func (s *QAStage) Pass(ctx context.Context) QAPassResult {
	var res QAPassResult
	var waiting []core.StateRecord
	err := s.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		records, err := tx.ListRecords(ctx, state.RecordFilter{States: []core.State{core.StateQAEvaluation}})
		for _, r := range records {
			if _, err := ReadQAResult(ctx, tx, r.RunID, r.Attempt); core.HasCode(err, core.CodeNotFound) {
				waiting = append(waiting, r)
			}
		}
		return err
	})
	if err != nil {
		return QAPassResult{Errors: []error{err}}
	}
	for _, record := range waiting {
		verdict, err := s.evaluate(ctx, record)
		switch {
		case err != nil && !core.HasCode(err, core.CodeLeaseHeld):
			res.Errors = append(res.Errors, fmt.Errorf("%s: %w", record.IssueID, err))
		case verdict == VerdictPassed:
			res.Passed = append(res.Passed, record.IssueID)
		case verdict == VerdictFailed:
			res.Failed = append(res.Failed, record.IssueID)
		case verdict == VerdictHeld:
			res.Held = append(res.Held, record.IssueID)
		}
	}
	return res
}

// evaluate judges one candidate and records the verdict under the lease.
// A judge or a repository that is unavailable leaves the candidate for a
// later pass; any other refusal is a failed evaluation.
func (s *QAStage) evaluate(ctx context.Context, record core.StateRecord) (string, error) {
	lease, err := s.take(ctx, record.IssueID)
	if err != nil {
		return "", err
	}
	defer s.letGo(ctx, lease)
	var doc handoff.CandidateDoc
	if err := s.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		doc, err = handoff.ReadCandidate(ctx, tx, record.RunID, record.Attempt)
		return err
	}); err != nil {
		return "", err
	}
	c := doc.Candidate
	if doc.BuildFailure != "" {
		return s.conclude(ctx, record, lease, QAResult{Candidate: c, Verdict: VerdictFailed, Reason: "the patch could not be built: " + doc.BuildFailure})
	}
	if c.IssueID != record.IssueID || c.Digest != record.CandidateDigest {
		return s.conclude(ctx, record, lease, QAResult{Candidate: c, Verdict: VerdictFailed, Reason: "the stored candidate is not the one the issue's run submitted"})
	}
	if c.ChangedPaths, err = s.Tree.ChangedPaths(ctx, c.BaselineSHA, c.CommitSHA); err != nil {
		return "", err
	}
	result, err := s.Evaluator.Run(ctx, c)
	switch {
	case core.HasCode(err, core.CodeUnavailable) || core.HasCode(err, core.CodeTimeout) || core.HasCode(err, core.CodeRateLimited):
		return "", err
	case err != nil:
		return s.conclude(ctx, record, lease, QAResult{Candidate: c, Verdict: VerdictFailed, Reason: "no receipt was issued: " + err.Error()})
	case result.Passed && result.Action == iqa.ActionRelease:
		return s.conclude(ctx, record, lease, QAResult{Candidate: c, Receipt: result.Receipt, Verdict: VerdictPassed})
	case result.Passed:
		return s.conclude(ctx, record, lease, QAResult{Candidate: c, Receipt: result.Receipt, Verdict: VerdictHeld,
			Reason: "QA passed on the coder's own model route; the Owner must approve the release"})
	}
	return s.conclude(ctx, record, lease, QAResult{Candidate: c, Receipt: result.Receipt, Verdict: VerdictFailed, Reason: result.Refusal.Error()})
}

// conclude writes the verdict and its step in one transaction. A failure
// with no attempt left is held: the lifecycle has no edge from QAEvaluation
// but back to development, and the run may not start another attempt.
func (s *QAStage) conclude(ctx context.Context, record core.StateRecord, lease state.Lease, r QAResult) (string, error) {
	r.At = s.Clock.Now().UTC()
	if r.Verdict == VerdictFailed && record.Attempt >= s.MaxAttempts {
		r.Verdict, r.Reason = VerdictHeld, fmt.Sprintf("attempt %d of %d failed QA: %s", record.Attempt, s.MaxAttempts, r.Reason)
	}
	t := core.Transition{
		IssueID: record.IssueID, From: core.StateQAEvaluation, ExpectedVersion: record.Version,
		LeaseOwner: lease.Owner, FencingToken: lease.Token, RunID: record.RunID, Attempt: record.Attempt,
		CandidateDigest: record.CandidateDigest, ActorID: s.Owner, Reason: r.Reason,
	}
	guards := statemachine.Guards{}
	switch r.Verdict {
	case VerdictPassed:
		t.To, t.Event, t.IdempotencyKey = core.StateReleaseCandidate, core.EventQAPassed, "qa-passed/"+string(record.RunID)+"/"+itoa(record.Attempt)
		proof, err := s.proof(ctx, r.Receipt)
		if err != nil {
			return "", err
		}
		guards.QA = statemachine.QAGuardFunc(func(context.Context, core.Transition) (statemachine.QAProof, error) { return proof, nil })
	case VerdictFailed:
		t.To, t.Event, t.IdempotencyKey, t.Attempt = core.StateDeveloping, core.EventQAFailed, iqa.QAFailedKey(record.RunID, record.Attempt), record.Attempt+1
		guards.Attempts = statemachine.AttemptsGuardFunc(func(context.Context, core.Transition) (statemachine.AttemptsProof, error) {
			return statemachine.AttemptsProof{IssueID: record.IssueID, RunID: record.RunID, Attempt: record.Attempt, MaxAttempts: s.MaxAttempts}, nil
		})
	}
	err := s.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		if err := tx.CheckFencing(ctx, lease.Resource, lease.Token); err != nil {
			return err
		}
		if err := put(ctx, tx, QAResultKey(record.RunID, record.Attempt), r); err != nil {
			return err
		}
		if r.Verdict == VerdictHeld {
			return nil
		}
		if err := statemachine.New(s.Clock, guards).Check(ctx, t); err != nil {
			return err
		}
		_, err := tx.Transition(ctx, t)
		return err
	})
	return r.Verdict, err
}

// proof is the receipt next to the policy, baseline and suite in force now.
func (s *QAStage) proof(ctx context.Context, receipt core.QAReceipt) (statemachine.QAProof, error) {
	refs, err := s.Policy(ctx)
	if err != nil {
		return statemachine.QAProof{}, err
	}
	gates := slices.Concat(core.RequiredHardGates(), s.ExtraGates)
	return statemachine.QAProof{
		Receipt: receipt, FactoryID: s.FactoryID, PolicyRevision: refs.PolicySHA,
		BaselineSHA: refs.PolicySHA, SuiteDigest: handoff.SuiteDigest(refs), RequiredGates: gates, PassScore: s.PassScore,
	}, nil
}

func (s *QAStage) take(ctx context.Context, issue core.IssueRef) (state.Lease, error) {
	return takeLease(ctx, s.State, issue, s.Owner, s.LeaseTTL)
}

func (s *QAStage) letGo(ctx context.Context, lease state.Lease) { letGo(ctx, s.State, lease) }

// takeLease acquires the issue lease for owner.
func takeLease(ctx context.Context, store state.Store, issue core.IssueRef, owner string, ttl time.Duration) (state.Lease, error) {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	var lease state.Lease
	err := store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		lease, err = tx.AcquireLease(ctx, state.IssueLeaseResource(issue), owner, ttl)
		return err
	})
	return lease, err
}

// letGo gives an issue lease up; a lease already lost needs nothing more.
func letGo(ctx context.Context, store state.Store, lease state.Lease) {
	_ = store.Update(context.WithoutCancel(ctx), func(ctx context.Context, tx state.Tx) error {
		return tx.ReleaseLease(ctx, lease)
	})
}
