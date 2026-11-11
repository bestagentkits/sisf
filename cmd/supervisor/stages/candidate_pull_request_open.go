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
	if c.namedByIDOnly() {
		return c.record(ctx, c.decided(record, noLogin+" and opens none"))
	}
	pr, foreign, why, err := c.openOnGitHub(ctx, issue, run, attempt, scope, qa, doc.Branch)
	switch {
	case err != nil && !transient(err):
		return c.record(ctx, c.decided(record, fmt.Sprintf("GitHub refused to find or open the candidate's pull request (status %d, %s)",
			github.ErrorStatus(err), core.CodeOf(err))))
	case err != nil:
		return handoff.CandidatePR{}, err
	case foreign != 0:
		return c.record(ctx, c.decided(record, fmt.Sprintf("pull request #%d on the candidate branch was not opened by the factory for this run (%s); "+
			"the factory neither adopts nor merges it", foreign, why)))
	}
	record.Number, record.URL = pr.Number, pr.URL
	return c.record(ctx, record)
}

// openOnGitHub finds the factory's pull request for run on branch, or
// opens it; foreign is the number of another one open or merged there, and
// why it is not the factory's, in which case none is opened. Every error
// is GitHub's.
func (c *CandidatePRs) openOnGitHub(ctx context.Context, issue core.IssueRef, run core.RunID, attempt int, scope core.Digest,
	qa QAResult, branch string) (pr github.PullRequest, foreign int, why string, err error) {
	existing, err := c.GitHub.FindPullRequests(ctx, c.Repo, branch)
	if err != nil {
		return github.PullRequest{}, 0, "", err
	}
	pr, ours, other, why, err := c.sortOut(ctx, run, existing)
	if err != nil || ours || other.Number != 0 {
		return pr, other.Number, why, err
	}
	title, err := c.title(ctx, issue)
	if err != nil {
		return github.PullRequest{}, 0, "", err
	}
	pr, err = c.GitHub.CreatePullRequest(ctx, c.Repo, github.NewPullRequest{
		Title: title, Body: prBody(issue, run, attempt, scope, qa), HeadRef: branch, BaseRef: c.Base,
	})
	if !core.HasCode(err, core.CodeAlreadyExists) {
		return pr, 0, "", err
	}
	// A pull request is open on the branch though none was listed: the
	// factory's own, opened by a try whose answer was lost, or one the
	// listing did not show yet. It is looked for again, and not given up
	// on while GitHub does not list it.
	if existing, err = c.GitHub.FindPullRequests(ctx, c.Repo, branch); err != nil {
		return github.PullRequest{}, 0, "", err
	}
	pr, ours, other, why, err = c.sortOut(ctx, run, existing)
	if err != nil || ours || other.Number != 0 {
		return pr, other.Number, why, err
	}
	return github.PullRequest{}, 0, "", core.NewError(core.CodeUnavailable,
		"GitHub says a pull request is open on "+branch+" but does not list it yet")
}

// sortOut finds, among the pull requests on a candidate's branch, the one
// the factory opened for run, and the first other one that is open or
// merged with why it is not the factory's. The factory's account is asked
// for only when the branch has pull requests.
func (c *CandidatePRs) sortOut(ctx context.Context, run core.RunID, prs []github.PullRequest) (github.PullRequest, bool, github.PullRequest, string, error) {
	if len(prs) == 0 {
		return github.PullRequest{}, false, github.PullRequest{}, "", nil
	}
	self, err := c.factoryAccount(ctx)
	if err != nil {
		return github.PullRequest{}, false, github.PullRequest{}, "", err
	}
	var foreign github.PullRequest
	why := ""
	for _, pr := range prs {
		reason := c.notOpened(self, run, pr)
		if reason == "" {
			return pr, true, github.PullRequest{}, "", nil
		}
		if foreign.Number == 0 && (pr.State == github.IssueOpen || pr.Merged) {
			foreign, why = pr, reason
		}
	}
	return github.PullRequest{}, false, foreign, why, nil
}

// notOpened says why pr is not the one the factory opened for run, "" when
// it is: its own account is the author, it targets the default branch and
// its body carries the run's marker. An author other than the configured
// account is named with it, so a wrong login in the install shows as one.
func (c *CandidatePRs) notOpened(self account, run core.RunID, pr github.PullRequest) string {
	switch {
	case !self.wrote(pr.Author):
		return fmt.Sprintf("its author `%s` is not the factory's configured account `%s`", pr.Author.Login, self.Login)
	case pr.BaseRef != c.Base:
		return "it does not target " + c.Base
	case !strings.Contains(pr.Body, candidateMarker(run)):
		return "it does not carry this run's marker"
	}
	return ""
}

// noLogin is why an install that names the factory's account by an ID
// alone merges nothing.
const noLogin = "the install names no login for the factory's GitHub account (extensions.github_app_login), " +
	"so the factory cannot tell its own pull request from anyone else's"

// namedByIDOnly reports whether the install names the factory's account by
// a numeric ID and no login.
func (c *CandidatePRs) namedByIDOnly() bool {
	c.selfMu.Lock()
	defer c.selfMu.Unlock()
	return c.self.ID != 0 && c.self.Login == ""
}

// account is the factory's own GitHub account as the supervisor knows it.
type account struct {
	github.User
	// userID: User.ID is the account's own user ID, as GitHub named it
	// when asked. An ID the install wrote down may be the App's instead.
	userID bool
}

// wrote reports whether author is this account. The login decides, and
// without one nothing matches: an install may name the App's numeric ID,
// and some other user's ID can equal it, so an ID never stands in for the
// login. An ID GitHub itself named only narrows the match.
func (a account) wrote(author github.User) bool {
	if a.Login == "" || !strings.EqualFold(author.Login, a.Login) {
		return false
	}
	return !a.userID || a.ID == 0 || author.ID == a.ID
}

// factoryAccount is the factory's own account: the one the install names
// (Config.Self), else the one the supervisor's token acts as, learned from
// GitHub once. An installation token of a GitHub App cannot ask GitHub
// this, so an App install must name its account.
func (c *CandidatePRs) factoryAccount(ctx context.Context) (account, error) {
	c.selfMu.Lock()
	defer c.selfMu.Unlock()
	if c.self.ID != 0 || c.self.Login != "" {
		return account{User: c.self, userID: c.selfAsked}, nil
	}
	u, err := c.GitHub.Viewer(ctx)
	if err != nil {
		return account{}, err
	}
	c.self, c.selfAsked = u, true
	return account{User: u, userID: true}, nil
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
