package core

import (
	"strings"
	"time"
)

// QACheckKind separates checks that can veto from checks that only score.
type QACheckKind string

const (
	// CheckHardGate is a deterministic gate; one failure blocks the release.
	CheckHardGate QACheckKind = "hard_gate"
	// CheckRubric is a scored rubric category.
	CheckRubric QACheckKind = "rubric"
)

// Valid reports whether k is a known check kind.
func (k QACheckKind) Valid() bool { return k == CheckHardGate || k == CheckRubric }

// QACheck is one result recorded in a receipt.
type QACheck struct {
	// Name is the hard gate or rubric category that was evaluated.
	Name   string      `json:"name"`
	Kind   QACheckKind `json:"kind"`
	Passed bool        `json:"passed"`
	// Points and MaxPoints carry the score of a rubric category.
	Points    int    `json:"points,omitempty"`
	MaxPoints int    `json:"max_points,omitempty"`
	Detail    string `json:"detail,omitempty"`
	// EvidenceRef points to the log or report behind the result.
	EvidenceRef string `json:"evidence_ref,omitempty"`
}

// QAReceipt is the signed statement of an independent evaluator about one
// built candidate. It is bound to the factory that judged, the issue and run
// the candidate was built for, the candidate's commit and digest, the test
// suite, the read-only baseline and the policy in force, so it cannot be
// reused for another factory, issue, run or commit, and it names both agents
// and both model routes, so self-scoring and shared blind spots are visible.
//
// The signature covers SigningPayload, which holds every field but the
// signature itself.
type QAReceipt struct {
	// FactoryID names the factory deployment whose evaluator judged the
	// candidate. Two factories on one upstream repository see the same issue
	// numbers, and a receipt of one never counts in the other.
	FactoryID FactoryID `json:"factory_id"`
	// IssueID and RunID name the issue and the run the candidate belongs to.
	IssueID      IssueRef  `json:"issue_id"`
	RunID        RunID     `json:"run_id"`
	CandidateSHA CommitSHA `json:"candidate_sha"`
	// CandidateDigest is the digest of the built candidate; code that has
	// not finished building has none and cannot be scored.
	CandidateDigest     Digest     `json:"candidate_digest"`
	SuiteDigest         Digest     `json:"suite_digest"`
	BaselineSHA         CommitSHA  `json:"baseline_sha"`
	PolicySHA           CommitSHA  `json:"policy_sha"`
	QAAgentIdentity     string     `json:"qa_agent_identity"`
	CodingAgentIdentity string     `json:"coding_agent_identity"`
	CodingModelRoute    ModelRoute `json:"coding_model_route"`
	QAModelRoute        ModelRoute `json:"qa_model_route"`
	// SameModelRoute discloses that QA ran on the coder's provider and model.
	SameModelRoute bool      `json:"same_model_route"`
	Checks         []QACheck `json:"checks"`
	// Score is the sum of the points of the rubric checks, within 0-100.
	Score            int       `json:"score"`
	IssuedAt         time.Time `json:"timestamp"`
	ExpiresAt        time.Time `json:"expires"`
	EvaluatorAttempt int       `json:"evaluator_attempt"`
	Signature        Signature `json:"signature"`
}

// HardGatesPassed reports whether at least one hard gate was recorded and
// none failed. The score plays no part: a high score never hides a failed
// gate, and a receipt without gate results proves nothing.
func (r QAReceipt) HardGatesPassed() bool {
	recorded := false
	for _, c := range r.Checks {
		if c.Kind != CheckHardGate {
			continue
		}
		if !c.Passed {
			return false
		}
		recorded = true
	}
	return recorded
}

// Expired reports whether the receipt may no longer authorize a release.
func (r QAReceipt) Expired(now time.Time) bool { return !now.Before(r.ExpiresAt) }

// Validate checks that the receipt is complete and internally consistent. It
// does not verify the signature or decide whether the candidate passes.
func (r QAReceipt) Validate() error {
	if err := r.FactoryID.Validate(); err != nil {
		return err
	}
	if err := r.IssueID.Validate(); err != nil {
		return err
	}
	if err := r.RunID.Validate(); err != nil {
		return err
	}
	for _, sha := range []CommitSHA{r.CandidateSHA, r.BaselineSHA, r.PolicySHA} {
		if err := sha.Validate(); err != nil {
			return err
		}
	}
	for _, d := range []Digest{r.CandidateDigest, r.SuiteDigest} {
		if err := d.Validate(); err != nil {
			return err
		}
	}
	if err := r.CodingModelRoute.Validate(); err != nil {
		return err
	}
	if err := r.QAModelRoute.Validate(); err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(r.QAAgentIdentity) == "" || strings.TrimSpace(r.CodingAgentIdentity) == "":
		return invalid("receipt must name both the QA agent and the coding agent")
	case SameIdentity(r.QAAgentIdentity, r.CodingAgentIdentity):
		return invalid("agent %q cannot evaluate its own candidate", r.QAAgentIdentity)
	case r.SameModelRoute != r.CodingModelRoute.SameModel(r.QAModelRoute):
		return invalid("same_model_route must state whether QA shares the coder's provider and model")
	case len(r.Checks) == 0:
		return invalid("receipt records no checks")
	case r.Score < 0 || r.Score > 100:
		return invalid("score %d must be within 0-100", r.Score)
	case r.IssuedAt.IsZero() || !r.ExpiresAt.After(r.IssuedAt):
		return invalid("receipt must expire after it is issued")
	case r.EvaluatorAttempt < 1:
		return invalid("evaluator attempt %d must be at least 1", r.EvaluatorAttempt)
	}
	for _, c := range r.Checks {
		switch {
		case c.Name == "":
			return invalid("receipt contains an unnamed check")
		case !c.Kind.Valid():
			return invalid("check %s has unknown kind %q", c.Name, string(c.Kind))
		case c.Points < 0 || c.Points > c.MaxPoints:
			return invalid("check %s scores %d outside 0-%d", c.Name, c.Points, c.MaxPoints)
		}
	}
	return r.Signature.Validate()
}

// SameIdentity reports whether two identities name the same party. Letter
// case and surrounding white space are ignored, so an identity cannot be made
// to look like another party by respelling it. Every check that keeps two
// roles apart, such as the coder and its evaluator, compares with it.
func SameIdentity(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// Domain and field-list version of the receipt's signing payload. Version 2
// added the factory.
const (
	receiptPayloadDomain  = "sisf.qa_receipt"
	receiptPayloadVersion = 2
)

// SigningPayload returns the canonical bytes the evaluator signs and a
// verifier checks. Every field except the signature is bound, in a fixed
// order, so changing any of them, or the order or content of the checks,
// yields different bytes.
func (r QAReceipt) SigningPayload() []byte {
	b := NewPayloadBuilder(receiptPayloadDomain, receiptPayloadVersion).
		Text("factory_id", string(r.FactoryID)).
		Text("issue_owner", r.IssueID.Owner).
		Text("issue_repo", r.IssueID.Repo).
		Int("issue_number", int64(r.IssueID.Number)).
		Text("run_id", string(r.RunID)).
		Text("candidate_sha", string(r.CandidateSHA)).
		Text("candidate_digest", string(r.CandidateDigest)).
		Text("suite_digest", string(r.SuiteDigest)).
		Text("baseline_sha", string(r.BaselineSHA)).
		Text("policy_sha", string(r.PolicySHA)).
		Text("qa_agent_identity", r.QAAgentIdentity).
		Text("coding_agent_identity", r.CodingAgentIdentity)
	writeRoute(b, "coding_model_route", r.CodingModelRoute)
	writeRoute(b, "qa_model_route", r.QAModelRoute)
	b.Bool("same_model_route", r.SameModelRoute).
		Int("checks", int64(len(r.Checks)))
	for _, c := range r.Checks {
		b.Text("check_name", c.Name).
			Text("check_kind", string(c.Kind)).
			Bool("check_passed", c.Passed).
			Int("check_points", int64(c.Points)).
			Int("check_max_points", int64(c.MaxPoints)).
			Text("check_detail", c.Detail).
			Text("check_evidence_ref", c.EvidenceRef)
	}
	return b.Int("score", int64(r.Score)).
		Time("issued_at", r.IssuedAt).
		Time("expires_at", r.ExpiresAt).
		Int("evaluator_attempt", int64(r.EvaluatorAttempt)).
		Bytes()
}

// writeRoute binds the three parts of a model route under one field prefix.
func writeRoute(b *PayloadBuilder, prefix string, route ModelRoute) {
	b.Text(prefix+"_provider", route.Provider).
		Text(prefix+"_model", route.Model).
		Text(prefix+"_effort", route.Effort)
}
