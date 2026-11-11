package stages

import (
	"context"
	"fmt"
	"strings"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// candidateMarker ends the body of the pull request the factory opens for
// a run's candidate. With the factory's own account as author it is how
// that pull request is told from any other on the same branch.
func candidateMarker(run core.RunID) string { return "<!-- sisf:candidate " + string(run) + " -->" }

// ensure returns the run's candidate pull request, opening it the first
// time. The pull request the factory itself opened for the run, found on
// GitHub by its branch, is taken over rather than opened again, so a
// restart never opens a second. Any other pull request on the branch is
// never adopted, merged or closed: when one is open or merged, or GitHub
// permanently refuses to open the factory's own, the candidate is recorded
// as left for the Owner, so its issue still closes on the verified
// release. An error means GitHub could not answer and nothing was decided.
func (c *CandidatePRs) ensure(ctx context.Context, issue core.IssueRef, run core.RunID, attempt int) (handoff.CandidatePR, error) {
	var doc handoff.CandidateDoc
	var qa QAResult
	var scope core.Digest
	var stored *handoff.CandidatePR
	err := c.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		pr, err := handoff.ReadCandidatePR(ctx, tx, run)
		if err == nil {
			stored = &pr
			return nil
		}
		if !core.HasCode(err, core.CodeNotFound) {
			return err
		}
		if qa, err = ReadQAResult(ctx, tx, run, attempt); err != nil {
			return err
		}
		if doc, err = handoff.ReadCandidate(ctx, tx, run, attempt); err != nil {
			return err
		}
		scope = scopeOf(ctx, tx, run)
		return nil
	})
	if err != nil {
		return handoff.CandidatePR{}, err
	}
	if stored != nil {
		return *stored, nil
	}
	if qa.Verdict != VerdictPassed || doc.Branch == "" || qa.Candidate.CommitSHA.Validate() != nil {
		return handoff.CandidatePR{}, core.NewError(core.CodeGuardFailed, "only a candidate QA passed has a pull request")
	}
	record := handoff.CandidatePR{
		IssueID: issue, RunID: run, Attempt: attempt, Branch: doc.Branch, Base: c.Base,
		Commit: qa.Candidate.CommitSHA, State: handoff.LandingOpen, OpenedAt: c.Clock.Now().UTC(),
	}
	existing, err := c.GitHub.FindPullRequests(ctx, c.Repo, doc.Branch)
	if err != nil {
		return handoff.CandidatePR{}, err
	}
	pr, ours, foreign, err := c.sortOut(ctx, run, existing)
	if err != nil {
		return handoff.CandidatePR{}, err
	}
	switch {
	case ours:
	case foreign != 0:
		return c.record(ctx, c.decided(record, fmt.Sprintf("pull request #%d on the candidate branch was not opened by the factory for this run; "+
			"the factory neither adopts nor merges it", foreign)))
	default:
		title, err := c.title(ctx, issue)
		if err != nil {
			return handoff.CandidatePR{}, err
		}
		pr, err = c.GitHub.CreatePullRequest(ctx, c.Repo, github.NewPullRequest{
			Title: title, Body: prBody(issue, run, attempt, scope, qa), HeadRef: doc.Branch, BaseRef: c.Base,
		})
		if err != nil && !transient(err) {
			return c.record(ctx, c.decided(record, fmt.Sprintf("GitHub refused to open the candidate's pull request (status %d)", github.ErrorStatus(err))))
		}
		if err != nil {
			return handoff.CandidatePR{}, err
		}
	}
	record.Number, record.URL = pr.Number, pr.URL
	return c.record(ctx, record)
}

// sortOut finds, among the pull requests on a candidate's branch, the one
// the factory opened for run, and the number of any other that is open or
// merged.
func (c *CandidatePRs) sortOut(ctx context.Context, run core.RunID, prs []github.PullRequest) (github.PullRequest, bool, int, error) {
	if len(prs) == 0 {
		return github.PullRequest{}, false, 0, nil
	}
	self, err := c.factoryAccount(ctx)
	if err != nil {
		return github.PullRequest{}, false, 0, err
	}
	foreign := 0
	for _, pr := range prs {
		if c.opened(self, run, pr) {
			return pr, true, 0, nil
		}
		if foreign == 0 && (pr.State == github.IssueOpen || pr.Merged) {
			foreign = pr.Number
		}
	}
	return github.PullRequest{}, false, foreign, nil
}

// opened reports whether pr is the one the factory opened for run: its
// own account is the author, it targets the default branch and its body
// carries the run's marker.
func (c *CandidatePRs) opened(self github.User, run core.RunID, pr github.PullRequest) bool {
	return sameAccount(self, pr.Author) && pr.BaseRef == c.Base && strings.Contains(pr.Body, candidateMarker(run))
}

// sameAccount reports whether author is the factory's account self.
func sameAccount(self, author github.User) bool {
	return self.ID != 0 && author.ID == self.ID || self.ID == 0 && self.Login != "" && strings.EqualFold(author.Login, self.Login)
}

// factoryAccount is the account the supervisor's token acts as, learned
// from GitHub once.
func (c *CandidatePRs) factoryAccount(ctx context.Context) (github.User, error) {
	c.selfMu.Lock()
	defer c.selfMu.Unlock()
	if c.self.ID != 0 || c.self.Login != "" {
		return c.self, nil
	}
	u, err := c.GitHub.Viewer(ctx)
	if err != nil {
		return github.User{}, err
	}
	c.self = u
	return u, nil
}

// decided is record left for the Owner for reason, with no pull request
// of the factory's.
func (c *CandidatePRs) decided(record handoff.CandidatePR, reason string) handoff.CandidatePR {
	record.State, record.Reason, record.DecidedAt = handoff.LandingLeftOpen, reason, c.Clock.Now().UTC()
	return record
}

func (c *CandidatePRs) record(ctx context.Context, pr handoff.CandidatePR) (handoff.CandidatePR, error) {
	return pr, c.put(ctx, pr)
}
