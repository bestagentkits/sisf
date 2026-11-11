package runactions

import (
	"context"
	"fmt"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// CandidatePR is the pull request of a run's passed candidate and what
// became of it, as runs.inspect and runs.explain show it.
type CandidatePR struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Branch string `json:"branch"`
	// State is open, pending (merging), merged, left_for_owner or
	// rolled_back.
	State       string    `json:"state"`
	Reason      string    `json:"reason,omitempty"`
	MergeCommit string    `json:"merge_commit,omitempty"`
	Ref         string    `json:"ref"`
	At          time.Time `json:"at,omitzero"`
}

// CandidatePRs reads the pull request of a run's candidate, NOT_FOUND
// when the run has none.
type CandidatePRs interface {
	CandidatePR(ctx context.Context, run core.RunID) (CandidatePR, error)
}

// candidatePR reads the pull request of r's run, nil when it has none or
// no reader is wired.
func (h handlers) candidatePR(ctx context.Context, r core.StateRecord) (*CandidatePR, error) {
	if h.CandidatePRs == nil || r.RunID == "" {
		return nil, nil
	}
	pr, err := h.CandidatePRs.CandidatePR(ctx, r.RunID)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return nil, nil
	case err != nil:
		return nil, err
	}
	return &pr, nil
}

// summary says in one line where the pull request stands.
func (pr CandidatePR) summary() string {
	switch pr.State {
	case "merged":
		return fmt.Sprintf("pull request #%d is merged", pr.Number)
	case "left_for_owner":
		return fmt.Sprintf("%s is not merged and left for the Owner: %s", pr.name(), pr.Reason)
	case "rolled_back":
		return fmt.Sprintf("pull request #%d is not merged: its release was rolled back", pr.Number)
	case "refused":
		return fmt.Sprintf("pull request #%d is not merged: its release was refused before anything was deployed", pr.Number)
	case "pending":
		return fmt.Sprintf("pull request #%d is being merged after its release was verified", pr.Number)
	}
	return fmt.Sprintf("pull request #%d is open until its release is verified", pr.Number)
}

// explainPullRequest cites the candidate's pull request and, for a
// finished run, says whether its change was merged: an issue closes on its
// verified deployment, whether or not the merge was made.
func explainPullRequest(out *Explanation, pr *CandidatePR) {
	if pr == nil {
		return
	}
	out.Evidence = append(out.Evidence, Evidence{Kind: "candidate_pr", Ref: pr.Ref, Summary: pr.summary(), At: pr.At})
	if out.Cause != CauseFinished {
		return
	}
	switch pr.State {
	case "merged":
		out.Reason += fmt.Sprintf(" The change is merged by pull request #%d.", pr.Number)
	case "left_for_owner":
		out.Reason += fmt.Sprintf(" The change is not merged: %s is left for the Owner (%s).", pr.name(), pr.Reason)
	}
}

// name names the pull request; the factory opened none when another was
// on the candidate's branch or GitHub refused to open it.
func (pr CandidatePR) name() string {
	if pr.Number == 0 {
		return "the candidate"
	}
	return fmt.Sprintf("pull request #%d", pr.Number)
}
