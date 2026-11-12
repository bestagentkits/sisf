package stages

import (
	"context"
	"encoding/json"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/internal/qa/agentjudge"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state"
)

// CandidateMaterials gathers what the QA agent is shown of a candidate from
// the supervisor's own records and the repository at exact commits: the
// run's approved scope and definition of done, the patch the agent zone
// exported for the attempt, the supervisor's build record, the review
// criteria at the pinned commit and the changed files at the candidate's
// commit. Nothing is taken from what the candidate says about itself.
type CandidateMaterials struct {
	State state.Store
	Tree  Tree
}

var _ agentjudge.MaterialsSource = CandidateMaterials{}

// Materials returns the materials of candidate c.
func (m CandidateMaterials) Materials(ctx context.Context, c qa.Candidate) (agentjudge.Materials, error) {
	var out agentjudge.Materials
	err := m.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, handoff.Namespace, "run/"+string(c.RunID))
		if err != nil {
			return err
		}
		out.Scope, out.AcceptanceCriteria, err = briefOf(entry.Value)
		if err != nil {
			return err
		}
		patch, err := handoff.ReadPatch(ctx, tx, c.RunID, c.Attempt)
		if err != nil {
			return err
		}
		if patch.IssueID != c.IssueID {
			return core.NewError(core.CodeTampered, "the stored patch belongs to another issue")
		}
		out.Diff = patch.Diff
		build, err := handoff.ReadBuild(ctx, tx, c.CommitSHA)
		if err != nil {
			return err
		}
		out.Build = agentjudge.BuildResult{Builder: build.Builder, TestsPassed: build.TestsPassed, Evidence: build.TestEvidence}
		return nil
	})
	if err != nil {
		return agentjudge.Materials{}, err
	}
	review, err := m.Tree.ReadFile(ctx, c.Policy.ReviewSHA, policy.PathReview)
	if err != nil {
		return agentjudge.Materials{}, err
	}
	out.ReviewCriteria = string(review)
	for _, path := range c.ChangedPaths {
		if len(out.Files) == agentjudge.MaxFiles {
			break
		}
		content, err := m.Tree.ReadFile(ctx, c.CommitSHA, path)
		switch {
		case core.HasCode(err, core.CodeNotFound):
			continue // deleted by the candidate; the diff shows it
		case err != nil:
			return agentjudge.Materials{}, err
		}
		out.Files = append(out.Files, agentjudge.File{Path: path, Content: content})
	}
	return out, nil
}

// briefOf reads the scope and the definition of done of a stored
// dispatcher run record.
func briefOf(raw []byte) (scope, done string, err error) {
	var run struct {
		Brief struct {
			Handoff struct {
				SoW string `json:"sow"`
				DoD string `json:"dod"`
			} `json:"handoff"`
		} `json:"brief"`
	}
	if json.Unmarshal(raw, &run) != nil {
		return "", "", core.NewError(core.CodeTampered, "the stored run record cannot be read")
	}
	return run.Brief.Handoff.SoW, run.Brief.Handoff.DoD, nil
}
