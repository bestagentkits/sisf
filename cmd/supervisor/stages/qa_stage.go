package stages

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/dispatcher"
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
	// Budget closes the reservation of a run QA passed or held; nil leaves
	// it to the dispatcher.
	Budget   dispatcher.ReservationCloser
	Clock    core.Clock
	LeaseTTL time.Duration
}

// QAPassResult is what one pass of the stage did, per verdict, with the
// reason of each verdict that did not pass.
type QAPassResult struct {
	Passed, Failed, Held []core.IssueRef
	Reasons              map[core.IssueRef]string
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
	res.Reasons = map[core.IssueRef]string{}
	for _, record := range waiting {
		r, err := s.evaluate(ctx, record)
		switch {
		case err != nil:
			if !core.HasCode(err, core.CodeLeaseHeld) {
				res.Errors = append(res.Errors, fmt.Errorf("%s: %w", record.IssueID, err))
			}
			continue
		case r.Verdict == VerdictPassed:
			res.Passed = append(res.Passed, record.IssueID)
		case r.Verdict == VerdictFailed:
			res.Failed = append(res.Failed, record.IssueID)
		case r.Verdict == VerdictHeld:
			res.Held = append(res.Held, record.IssueID)
		}
		res.Reasons[record.IssueID] = r.Reason
		// The verdict is recorded; closing the reservation is a step of its
		// own, so a budget failure never undoes a paid judgment.
		if r.Verdict != VerdictFailed {
			if err := s.closeRun(ctx, record.RunID); err != nil {
				res.Errors = append(res.Errors, fmt.Errorf("%s: close the run's reservation: %w", record.IssueID, err))
			}
		}
	}
	return res
}

// evaluate judges one candidate and records the verdict under the lease.
// A judge or a repository that is unavailable leaves the candidate for a
// later pass; any other refusal is a failed evaluation.
func (s *QAStage) evaluate(ctx context.Context, record core.StateRecord) (QAResult, error) {
	lease, err := s.take(ctx, record.IssueID)
	if err != nil {
		return QAResult{}, err
	}
	defer s.letGo(ctx, lease)
	var doc handoff.CandidateDoc
	if err := s.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		doc, err = handoff.ReadCandidate(ctx, tx, record.RunID, record.Attempt)
		return err
	}); err != nil {
		return QAResult{}, err
	}
	c := doc.Candidate
	if doc.BuildFailure != "" {
		return s.conclude(ctx, record, lease, QAResult{Candidate: c, Verdict: VerdictFailed, Reason: "the patch could not be built: " + doc.BuildFailure})
	}
	if c.IssueID != record.IssueID || c.Digest != record.CandidateDigest {
		return s.conclude(ctx, record, lease, QAResult{Candidate: c, Verdict: VerdictFailed, Reason: "the stored candidate is not the one the issue's run submitted"})
	}
	listed, err := s.Tree.ChangedPaths(ctx, c.BaselineSHA, c.CommitSHA)
	switch {
	case core.HasCode(err, core.CodeLimitExceeded):
		// The listing may be cut short: the candidate is judged on what is
		// listed, and release governance then leaves it for the Owner.
		c.ChangedPathsIncomplete = true
	case err != nil:
		return QAResult{}, err
	}
	// The builder's list is every path of the patch; GitHub's adds what
	// differs from the baseline besides. The gates, the release governance
	// and the merge see both.
	c.ChangedPaths = unionPaths(c.ChangedPaths, listed)
	result, err := s.Evaluator.Run(ctx, c)
	switch {
	case core.HasCode(err, core.CodeUnavailable) || core.HasCode(err, core.CodeTimeout) || core.HasCode(err, core.CodeRateLimited):
		return QAResult{}, err
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
// but back to development, and the run may not start another attempt. A
// held verdict writes no step; its hold is recorded in the same
// transaction. Every verdict leaves a note the issue's progress shows.
func (s *QAStage) conclude(ctx context.Context, record core.StateRecord, lease state.Lease, r QAResult) (QAResult, error) {
	r.At = s.Clock.Now().UTC()
	hold := handoff.QAHoldOwnerApproval
	if r.Verdict == VerdictFailed && record.Attempt >= s.MaxAttempts {
		r.Verdict, r.Reason = VerdictHeld, fmt.Sprintf("attempt %d of %d failed QA: %s", record.Attempt, s.MaxAttempts, r.Reason)
		hold = handoff.QAHoldAttemptsUsed
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
			return QAResult{}, err
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
		note := s.noteOf(record, r)
		if err := handoff.PutQANote(ctx, tx, note); err != nil {
			return err
		}
		if r.Verdict == VerdictHeld {
			return handoff.PutQAHold(ctx, tx, holdOf(note, hold))
		}
		if err := statemachine.New(s.Clock, guards).Check(ctx, t); err != nil {
			return err
		}
		_, err := tx.Transition(ctx, t)
		return err
	})
	return r, err
}

// noteOf is the issue's view of a verdict: gates and numbers only.
func (s *QAStage) noteOf(record core.StateRecord, r QAResult) handoff.QANote {
	n := handoff.QANote{
		IssueID: record.IssueID, RunID: record.RunID, Attempt: record.Attempt, MaxAttempts: s.MaxAttempts, Verdict: r.Verdict,
		Built: r.Candidate.Digest != "", Receipt: !r.Receipt.Signature.IsZero(), Score: r.Receipt.Score, PassScore: s.PassScore, At: r.At,
	}
	for _, c := range r.Receipt.Checks {
		if c.Kind == core.CheckHardGate && !c.Passed {
			n.FailedGates = append(n.FailedGates, c.Name)
		}
	}
	return n
}

// holdOf is the Owner's view of a held verdict.
func holdOf(n handoff.QANote, cause handoff.QAHoldCause) handoff.QAHold {
	return handoff.QAHold{
		IssueID: n.IssueID, RunID: n.RunID, Attempt: n.Attempt, MaxAttempts: n.MaxAttempts, Cause: cause,
		Built: n.Built, Receipt: n.Receipt, FailedGates: n.FailedGates, Score: n.Score, PassScore: n.PassScore, HeldAt: n.At,
	}
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

// unionPaths is the sorted set of the paths of a and b.
func unionPaths(a, b []string) []string {
	out := slices.Concat(a, b)
	slices.Sort(out)
	return slices.Compact(out)
}
