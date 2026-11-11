package stages

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// changedPathsFromGitHub lists the paths a candidate changes from GitHub's
// comparison of the policy commit its QA receipt names with the candidate
// commit the receipt names, as the supervisor asks it now. The stored
// candidate is a document of the control zone, which the supervisor shares
// with factoryd, so its list of paths never decides what is released or
// merged: its paths are only added to GitHub's, so that a path a rename
// left behind, which GitHub's listing does not name, is judged too, and a
// stored list can only make the decision stricter, never leave a path out.
//
// The candidate returned is c with those paths. It is marked as listed in
// part, which release governance and the merge refuse for the Owner, when
// GitHub's listing may be cut short or GitHub refuses the comparison. An
// error is returned only when GitHub could not answer: nothing is decided
// and the caller tries again later.
func changedPathsFromGitHub(ctx context.Context, tree interface {
	ChangedPaths(ctx context.Context, base, head core.CommitSHA) ([]string, error)
}, receipt core.QAReceipt, c qa.Candidate) (qa.Candidate, error) {
	listed, err := tree.ChangedPaths(ctx, receipt.PolicySHA, receipt.CandidateSHA)
	switch {
	case err == nil:
	case transient(err):
		return qa.Candidate{}, err
	default:
		// A listing cut short at GitHub's limit, or a comparison GitHub
		// refused: the change cannot be shown to stay within its unit.
		c.ChangedPathsIncomplete = true
	}
	c.ChangedPaths = unionPaths(listed, c.ChangedPaths)
	return c, nil
}
