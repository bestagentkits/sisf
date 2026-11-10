package statemachine

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// A guard supplies the facts about one guarded step; the machine, not the
// guard, decides whether those facts are enough. A guard therefore cannot
// let a step through by answering carelessly: an empty proof is refused.
//
// Every guard receives the requested transition so it can look the facts up
// for that issue and run. An error means the facts could not be established
// and the step is refused. A typed *core.Error keeps its code, so a refusal
// such as BUDGET_EXCEEDED or LEASE_HELD reaches the caller unchanged.
//
// A proof states both sides of every comparison the machine makes: what a
// signed record says and what is in force now, read from the factory's own
// configuration and stores. The value in force is never copied from the
// record it is compared with.

// ClaimProof is the lease and the budget reservation a claim is made under.
type ClaimProof struct {
	// LeaseOwner and FencingToken identify the lease on the issue. They must
	// be the ones the transition is written under.
	LeaseOwner   string
	FencingToken core.FencingToken
	// LeaseExpiresAt is when the lease lapses; a lapsed lease proves nothing.
	LeaseExpiresAt time.Time
	// Reservation is the budget admitted for the run. It exists in every
	// budget mode. Its amount may be zero when the mode is unlimited and must
	// be positive when the mode is capped: a cap that sets nothing aside
	// admits a run the budget never paid for.
	Reservation core.Reservation
}

// ClaimGuard proves that a claim holds both a lease and a budget reservation.
// The two are taken in the same store transaction as the claim, so the caller
// that claims is normally the one that answers.
type ClaimGuard interface {
	ClaimProof(ctx context.Context, t core.Transition) (ClaimProof, error)
}

// AttemptsProof is how many attempts a run has made against its limit.
type AttemptsProof struct {
	// IssueID and RunID name the record the count was read from. They must be
	// the issue and the run of the transition.
	IssueID core.IssueRef
	RunID   core.RunID
	// Attempt is the number of attempts the run has made, counting from 1 and
	// including the one that just failed evaluation. It is read from the
	// stored record, never taken from the request.
	Attempt int
	// MaxAttempts is the limit policy sets for one run. It applies in every
	// budget mode, and a proof that states none is refused.
	MaxAttempts int
}

// AttemptsGuard proves that a run sent back to Developing has attempts left.
// The retry must carry the next attempt number, so the count cannot be reset
// or held still to loop without end.
type AttemptsGuard interface {
	AttemptsProof(ctx context.Context, t core.Transition) (AttemptsProof, error)
}

// QAProof is the evaluator's receipt for the candidate, next to the factory,
// the policy, the baseline, the suite and the criteria in force now.
type QAProof struct {
	Receipt core.QAReceipt
	// FactoryID is the factory deployment this machine serves. The receipt
	// must have been issued by that factory's evaluator.
	FactoryID core.FactoryID
	// PolicyRevision is the policy commit in force now. A receipt issued
	// under a policy that has since been replaced releases nothing.
	PolicyRevision core.CommitSHA
	// BaselineSHA is the commit of the read-only QA baseline in force now,
	// and SuiteDigest the digest of the test suite in force now. A receipt
	// judged against an older baseline or another suite releases nothing.
	BaselineSHA core.CommitSHA
	SuiteDigest core.Digest
	// RequiredGates names every hard gate the review criteria demand. It must
	// contain each gate of core.RequiredHardGates and may add more; criteria
	// that leave a required gate out are refused, so the criteria can raise
	// the bar and never lower it. Each gate must appear in the receipt as a
	// passed hard gate.
	RequiredGates []string
	// PassScore is the minimum score, within 1-100, from the review criteria.
	PassScore int
}

// QAGuard proves that an independent evaluation passed. The implementation
// must return only a receipt whose signature it has verified, over the
// receipt's SigningPayload, against the trusted evaluator keys; the machine
// cannot check that. The machine checks everything else: that the receipt is
// complete, bound to the issue, run and candidate of the transition and to
// the factory, policy, baseline and suite in force, issued in the past and
// unexpired, and that it passes core.QAReceipt.CheckPass.
type QAGuard interface {
	QAProof(ctx context.Context, t core.Transition) (QAProof, error)
}

// DoDResult is the outcome of one item of the Definition of Done.
type DoDResult struct {
	Item        string
	Met         bool
	EvidenceRef string
}

// PostDeployProof is what was seen of a release after it went live.
type PostDeployProof struct {
	// IssueID and RunID name the issue and the run the release was made for.
	// They must be those of the transition.
	IssueID core.IssueRef
	RunID   core.RunID
	// Released is the digest the release set out to run. It must be the
	// release digest the transition names.
	Released core.ReleaseDigest
	// Observed is the digest read from the running system, never taken from
	// the result of the deploy call.
	Observed core.ReleaseDigest
	// Healthy is the health of the running revision at ObservedAt, which
	// must not lie after the current instant.
	Healthy    bool
	ObservedAt time.Time
	// WindowEndsAt closes the observation window in which a regression would
	// have rolled the release back.
	WindowEndsAt time.Time
	// DoD holds one result per item of the Definition of Done.
	DoD []DoDResult
}

// PostDeployGuard proves that the deployed revision was observed and meets
// the Definition of Done. A merged pull request is not such a proof.
type PostDeployGuard interface {
	PostDeployProof(ctx context.Context, t core.Transition) (PostDeployProof, error)
}

// Guards holds the guard of each guarded edge. A nil guard refuses its edge,
// so an edge can never pass because nobody wired its check.
type Guards struct {
	Delegation DelegationGuard
	Approval   ApprovalGuard
	Claim      ClaimGuard
	Attempts   AttemptsGuard
	QA         QAGuard
	Rollout    RolloutGuard
	Canary     CanaryGuard
	PostDeploy PostDeployGuard
	Retry      RetryGuard
}

// ClaimGuardFunc adapts a function to ClaimGuard, which suits a caller that
// holds the lease and the reservation inside its own transaction.
type ClaimGuardFunc func(ctx context.Context, t core.Transition) (ClaimProof, error)

// ClaimProof calls f.
func (f ClaimGuardFunc) ClaimProof(ctx context.Context, t core.Transition) (ClaimProof, error) {
	return f(ctx, t)
}

// AttemptsGuardFunc adapts a function to AttemptsGuard.
type AttemptsGuardFunc func(ctx context.Context, t core.Transition) (AttemptsProof, error)

// AttemptsProof calls f.
func (f AttemptsGuardFunc) AttemptsProof(ctx context.Context, t core.Transition) (AttemptsProof, error) {
	return f(ctx, t)
}

// QAGuardFunc adapts a function to QAGuard.
type QAGuardFunc func(ctx context.Context, t core.Transition) (QAProof, error)

// QAProof calls f.
func (f QAGuardFunc) QAProof(ctx context.Context, t core.Transition) (QAProof, error) {
	return f(ctx, t)
}

// PostDeployGuardFunc adapts a function to PostDeployGuard.
type PostDeployGuardFunc func(ctx context.Context, t core.Transition) (PostDeployProof, error)

// PostDeployProof calls f.
func (f PostDeployGuardFunc) PostDeployProof(ctx context.Context, t core.Transition) (PostDeployProof, error) {
	return f(ctx, t)
}
