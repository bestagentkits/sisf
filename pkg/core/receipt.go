package core

import "time"

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
// built candidate. It is bound to the candidate's commit and digest, the test
// suite, the read-only baseline and the policy in force, so it cannot be
// reused for another commit, and it names both agents and both model routes,
// so self-scoring and shared blind spots are visible.
type QAReceipt struct {
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
	SameModelRoute   bool      `json:"same_model_route"`
	Checks           []QACheck `json:"checks"`
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
	case r.QAAgentIdentity == "" || r.CodingAgentIdentity == "":
		return invalid("receipt must name both the QA agent and the coding agent")
	case r.QAAgentIdentity == r.CodingAgentIdentity:
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
