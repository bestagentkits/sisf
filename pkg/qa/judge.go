package qa

import (
	"context"
	"fmt"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Baseline is the read-only test baseline an evaluator judges from. It is
// locked to a commit and to the policy in force, so the candidate under test
// cannot change the suite or the criteria it is judged by.
type Baseline struct {
	// CommitSHA is the commit the baseline suite was read at.
	CommitSHA core.CommitSHA `json:"commit_sha"`
	// PolicySHA is the commit of the policy the baseline was locked under.
	PolicySHA core.CommitSHA `json:"policy_sha"`
	// SuiteDigest is the digest of the deterministic suite in the baseline.
	SuiteDigest core.Digest `json:"suite_digest"`
	// WorkspaceID names the evaluator's own workspace holding the baseline.
	WorkspaceID string `json:"workspace_id"`
	// ReadOnly is true only when the workspace is mounted without write access.
	ReadOnly bool `json:"read_only"`
}

// Validate requires a pinned, read-only baseline in a named workspace.
func (b Baseline) Validate() error {
	for _, sha := range []core.CommitSHA{b.CommitSHA, b.PolicySHA} {
		if err := sha.Validate(); err != nil {
			return err
		}
	}
	if err := b.SuiteDigest.Validate(); err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(b.WorkspaceID) == "":
		return core.NewError(core.CodeInvalidArgument, "the QA baseline must name the workspace it is mounted in")
	case !b.ReadOnly:
		return core.NewError(core.CodeInvalidArgument, "the QA baseline must be mounted read-only")
	}
	return nil
}

// JudgeRequest asks a judge to score one candidate against the rubric.
type JudgeRequest struct {
	Candidate Candidate `json:"candidate"`
	Baseline  Baseline  `json:"baseline"`
	// Identity is the evaluator's identity, never the coder's.
	Identity string `json:"identity"`
	// ModelRoute is the route the judge must run on.
	ModelRoute core.ModelRoute `json:"model_route"`
	Rubric     Rubric          `json:"rubric"`
}

// Scorecard is the rubric points a judge gives a candidate.
type Scorecard struct {
	Points map[Category]int `json:"points"`
	// Details explains the points of a category, when the judge says why.
	Details     map[Category]string `json:"details,omitempty"`
	EvidenceRef string              `json:"evidence_ref,omitempty"`
}

// Validate requires one score for every category of the rubric, within
// 0 and the category's weight, and nothing else.
func (s Scorecard) Validate(r Rubric) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if len(s.Points) != len(r.Weights) {
		return core.NewError(core.CodeInvalidArgument, "the scorecard must score exactly the categories of the rubric")
	}
	for category, weight := range r.Weights {
		points, ok := s.Points[category]
		if !ok || points < 0 || points > weight {
			return core.NewError(core.CodeInvalidArgument,
				fmt.Sprintf("the scorecard must score %s within 0-%d", category, weight))
		}
	}
	return nil
}

// Judge scores a candidate on the rubric categories. It is the semantic part
// of an evaluation; the hard gates are deterministic and run apart from it.
// An error means the candidate could not be scored.
type Judge interface {
	Score(ctx context.Context, req JudgeRequest) (Scorecard, error)
}
