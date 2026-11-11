package stages

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Land merges the pull request of a verified release's candidate, once,
// and only when all of these still hold:
//
//   - the release is the build of the candidate commit QA judged;
//   - the candidate changes no foundation, policy, CI, ownership or
//     migration file (those are the Owner's to merge, see ADR 0003);
//   - the policy in force is the one the run was pinned to;
//   - the pull request is open, targets the default branch and its head
//     is still exactly the candidate commit; GitHub checks the head again
//     as it merges.
//
// When one does not hold, or GitHub refuses the merge, the pull request is
// left open for the Owner, the reason is recorded and noted on it, and the
// issue still closes: it closes on the verified deployment, not on the
// merge. An outage returns an error and decides nothing; the merge is tried
// again before the issue closes. A pull request already merged at the
// candidate commit, as after a restart in the middle of a merge, counts as
// merged and is not merged again.
func (c *CandidatePRs) Land(ctx context.Context, req release.LandRequest) (release.Landing, error) {
	pr, err := c.ensure(ctx, req.IssueID, req.RunID, req.Candidate.Attempt)
	if err != nil {
		return release.Landing{}, err
	}
	if pr.State.Final() {
		return landingOf(pr), nil
	}
	if pr.State != handoff.LandingPending {
		pr.State = handoff.LandingPending
		if err := c.put(ctx, pr); err != nil {
			return release.Landing{}, err
		}
	}
	live, ok, err := c.live(ctx, pr)
	switch {
	case err != nil && !transient(err):
		return c.leave(ctx, pr, fmt.Sprintf("GitHub refused to show the pull request (status %d, %s)", github.ErrorStatus(err), core.CodeOf(err)))
	case err != nil:
		return release.Landing{}, err
	}
	// A merge already made at the candidate commit, as after a restart in
	// the middle of one, stands whatever changed since.
	if ok && live.Merged && live.HeadSHA == pr.Commit {
		return c.decide(ctx, pr, handoff.LandingMerged, "", "")
	}
	reason, err := c.mayNotMerge(ctx, req, pr)
	if err != nil {
		return release.Landing{}, err
	}
	if reason != "" {
		return c.leave(ctx, pr, reason)
	}
	self, err := c.factoryAccount(ctx)
	switch {
	case err != nil && !transient(err):
		return c.leave(ctx, pr, fmt.Sprintf("GitHub refused to name the factory's own account (status %d, %s)", github.ErrorStatus(err), core.CodeOf(err)))
	case err != nil:
		return release.Landing{}, err
	}
	switch {
	case self.Login == "":
		return c.leave(ctx, pr, noLogin)
	case !ok:
		return c.leave(ctx, pr, "the pull request is no longer on GitHub")
	case c.notOpened(self, req.RunID, live) != "":
		return c.leave(ctx, pr, "the pull request is no longer the one the factory opened for this run: "+c.notOpened(self, req.RunID, live))
	case live.Merged:
		return c.leave(ctx, pr, "the pull request was merged at another commit than the candidate")
	case live.State != github.IssueOpen:
		return c.leave(ctx, pr, "the pull request was closed")
	case live.HeadSHA != pr.Commit:
		return c.leave(ctx, pr, "the branch moved after QA judged the candidate")
	}
	merged, err := c.Merger.Merge(ctx, c.Repo, github.MergeRequest{
		Number: pr.Number, HeadSHA: pr.Commit,
		Title:   fmt.Sprintf("Merge the verified candidate of %s for #%d", pr.RunID, pr.IssueID.Number),
		Message: fmt.Sprintf("Release %s of this commit was verified in production; QA receipt score %d.", req.Release, req.Receipt.Score),
	})
	switch {
	case err == nil:
		return c.decide(ctx, pr, handoff.LandingMerged, "", merged.SHA)
	case transient(err):
		return release.Landing{}, err
	}
	// A refusal may answer a retry of a merge GitHub had already made, its
	// first answer lost: the pull request tells.
	if again, ok, findErr := c.live(ctx, pr); findErr != nil {
		return release.Landing{}, findErr
	} else if ok && again.Merged && again.HeadSHA == pr.Commit {
		return c.decide(ctx, pr, handoff.LandingMerged, "", "")
	}
	switch {
	case core.HasCode(err, core.CodeConflict):
		return c.leave(ctx, pr, "the branch moved after QA judged the candidate")
	case tryAgain(err) && pr.Retries < maxMergeRetries:
		pr.Retries++
		if putErr := c.put(ctx, pr); putErr != nil {
			return release.Landing{}, putErr
		}
		return release.Landing{}, core.NewError(core.CodeUnavailable, "GitHub asked to try the merge again").WithCause(err)
	}
	return c.leave(ctx, pr, fmt.Sprintf("GitHub refused the merge (status %d)", github.ErrorStatus(err)))
}

// maxMergeRetries bounds the merges tried again because GitHub asked to,
// one per round; then the refusal is final.
const maxMergeRetries = 5

// tryAgain reports whether GitHub refused the merge for now only: the
// base branch was modified just before, or GitHub has not yet worked out
// whether the pull request can be merged.
func tryAgain(err error) bool {
	if github.ErrorStatus(err) != http.StatusMethodNotAllowed {
		return false
	}
	msg := strings.ToLower(github.ErrorMessage(err))
	return strings.Contains(msg, "base branch was modified") || strings.Contains(msg, "try the merge again") ||
		strings.Contains(msg, "not mergeable")
}

// live reads the pull request as GitHub shows it now.
func (c *CandidatePRs) live(ctx context.Context, pr handoff.CandidatePR) (github.PullRequest, bool, error) {
	found, err := c.GitHub.FindPullRequests(ctx, c.Repo, pr.Branch)
	if err != nil {
		return github.PullRequest{}, false, err
	}
	live, ok := pullNumbered(found, pr.Number)
	return live, ok, nil
}

// mayNotMerge says why the factory may not merge this candidate, "" when
// it may. An error means the policy in force could not be read.
func (c *CandidatePRs) mayNotMerge(ctx context.Context, req release.LandRequest, pr handoff.CandidatePR) (string, error) {
	var build handoff.BuildRecord
	err := c.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		build, err = handoff.ReadBuild(ctx, tx, pr.Commit)
		return err
	})
	if err != nil && !core.HasCode(err, core.CodeNotFound) {
		return "", err
	}
	if err != nil || string(build.Digest) != string(req.Release) || req.Candidate.CommitSHA != pr.Commit {
		return "the verified release is not the build of the pull request's candidate commit", nil
	}
	if req.Candidate.ChangedPathsIncomplete {
		return "GitHub may list only part of the paths it changes, so only the Owner merges it", nil
	}
	var touched []string
	for _, p := range req.Candidate.ChangedPaths {
		if foundationOrProtected(p) {
			touched = append(touched, p)
		}
	}
	if len(touched) > 0 {
		return "it changes " + strings.Join(touched, ", ") + ", which only the Owner merges", nil
	}
	refs, err := c.Policy(ctx)
	if err != nil {
		return "", err
	}
	if refs.PolicySHA != req.Receipt.PolicySHA {
		return "the policy in force changed after the run was pinned to it", nil
	}
	return "", nil
}

// foundationOrProtected reports whether a path is one an agent's patch may
// not change or a foundation file the release governance keeps for the
// Owner.
func foundationOrProtected(p string) bool {
	if _, protected := ProtectedPath(p); protected {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimPrefix(p, "/")), "docs/adr/")
}

// leave records that the pull request is left for the Owner and why, and
// notes it on the pull request. The note is best effort: the decision
// stands without it.
func (c *CandidatePRs) leave(ctx context.Context, pr handoff.CandidatePR, reason string) (release.Landing, error) {
	landing, err := c.decide(ctx, pr, handoff.LandingLeftOpen, reason, "")
	if err == nil {
		_ = c.note(ctx, pr, "The release built from this pull request was verified, but the factory did not merge it: "+reason+". It is left open for the Owner.")
	}
	return landing, err
}

// decide records the final state of the pull request.
func (c *CandidatePRs) decide(ctx context.Context, pr handoff.CandidatePR, s handoff.LandingState, reason string, merge core.CommitSHA) (release.Landing, error) {
	pr.State, pr.Reason, pr.MergeCommit, pr.DecidedAt = s, reason, merge, c.Clock.Now().UTC()
	if err := c.put(ctx, pr); err != nil {
		return release.Landing{}, err
	}
	return landingOf(pr), nil
}

func (c *CandidatePRs) put(ctx context.Context, pr handoff.CandidatePR) error {
	return c.State.Update(ctx, func(ctx context.Context, tx state.Tx) error { return handoff.PutCandidatePR(ctx, tx, pr) })
}

// landingOf is the decision as the closing step states it.
func landingOf(pr handoff.CandidatePR) release.Landing {
	ref := handoff.BuildNamespace + ":" + handoff.CandidatePRKey(pr.RunID)
	if pr.State == handoff.LandingMerged {
		return release.Landing{Merged: true, Ref: ref, Summary: fmt.Sprintf("the change is merged on %s by pull request #%d", pr.Base, pr.Number)}
	}
	if pr.Number == 0 {
		return release.Landing{Ref: ref, Summary: fmt.Sprintf("the change is NOT merged: the factory opened no pull request for it, left for the Owner (%s)", pr.Reason)}
	}
	return release.Landing{Ref: ref, Summary: fmt.Sprintf("the change is NOT merged: pull request #%d is left for the Owner (%s)", pr.Number, pr.Reason)}
}

func pullNumbered(prs []github.PullRequest, number int) (github.PullRequest, bool) {
	for _, pr := range prs {
		if pr.Number == number {
			return pr, true
		}
	}
	return github.PullRequest{}, false
}

// transient reports whether GitHub could not answer, as opposed to
// refusing. A refused token (401) counts as not answering, so a token that
// is revoked or expired never decides a landing: the client holds the
// token it was started with and does not renew it, so such an issue stays
// in Verified, its landing due, until the operator replaces the token and
// restarts the supervisor.
func transient(err error) bool {
	return core.HasCode(err, core.CodeUnavailable) || core.HasCode(err, core.CodeRateLimited) ||
		core.HasCode(err, core.CodeTimeout) || core.HasCode(err, core.CodeInternal) || core.HasCode(err, core.CodeUnauthenticated)
}
