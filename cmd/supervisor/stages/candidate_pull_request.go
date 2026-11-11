package stages

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Merger merges a pull request at its exact head; github.Merger is one,
// and only the supervisor builds it.
type Merger interface {
	Merge(ctx context.Context, repo github.RepoRef, req github.MergeRequest) (github.Merged, error)
}

// CandidatePRs keeps the pull request of each candidate QA passed: it
// opens one from the candidate's branch to the default branch, lands it
// once the candidate's release is verified (see Land), and notes on it a
// release that was rolled back, which the factory never merges. Candidates
// QA failed or held get none.
type CandidatePRs struct {
	Repo github.RepoRef
	// Base is the default branch the pull requests target.
	Base   string
	State  state.Store
	GitHub GitHub
	Merger Merger
	// Policy is the policy in force; a candidate is merged only while it
	// is the one the run was pinned to.
	Policy PolicyRefs
	Clock  core.Clock
}

// CandidatePRPassResult is what one pass did.
type CandidatePRPassResult struct {
	Opened     []core.IssueRef
	RolledBack []core.IssueRef
	Errors     []error
}

// releasing are the states of a run whose candidate QA passed.
var releasing = []core.State{core.StateReleaseCandidate, core.StateCanary, core.StateMonitoring, core.StateVerified}

// Pass opens the missing pull requests of passed candidates and notes the
// ones whose release was rolled back.
func (c *CandidatePRs) Pass(ctx context.Context) CandidatePRPassResult {
	var res CandidatePRPassResult
	var open, rolledBack []core.StateRecord
	err := c.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		records, err := tx.ListRecords(ctx, state.RecordFilter{States: append(releasing, core.StateRolledBack, core.StateBlocked)})
		if err != nil {
			return err
		}
		for _, r := range records {
			if r.RunID == "" {
				continue
			}
			pr, err := handoff.ReadCandidatePR(ctx, tx, r.RunID)
			switch {
			case core.HasCode(err, core.CodeNotFound) && (r.State != core.StateRolledBack && r.State != core.StateBlocked):
				if qa, err := ReadQAResult(ctx, tx, r.RunID, r.Attempt); err == nil && qa.Verdict == VerdictPassed {
					open = append(open, r)
				}
			case err == nil && (r.State == core.StateRolledBack || r.State == core.StateBlocked) && !pr.State.Final():
				rolledBack = append(rolledBack, r)
			}
		}
		return nil
	})
	if err != nil {
		return CandidatePRPassResult{Errors: []error{err}}
	}
	for _, r := range open {
		if _, err := c.ensure(ctx, r.IssueID, r.RunID, r.Attempt); err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("%s: open the candidate's pull request: %w", r.IssueID, err))
			continue
		}
		res.Opened = append(res.Opened, r.IssueID)
	}
	for _, r := range rolledBack {
		if err := c.leaveRolledBack(ctx, r.RunID); err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("%s: note the rollback on the pull request: %w", r.IssueID, err))
			continue
		}
		res.RolledBack = append(res.RolledBack, r.IssueID)
	}
	return res
}

// ensure returns the run's candidate pull request, opening it the first
// time. One already on GitHub for the branch, open or closed, is taken
// over rather than opened again, so a restart never opens a second.
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
	if err != nil || stored != nil {
		if stored != nil {
			return *stored, nil
		}
		return handoff.CandidatePR{}, err
	}
	if qa.Verdict != VerdictPassed || doc.Branch == "" || qa.Candidate.CommitSHA.Validate() != nil {
		return handoff.CandidatePR{}, core.NewError(core.CodeGuardFailed, "only a candidate QA passed has a pull request")
	}
	existing, err := c.GitHub.FindPullRequests(ctx, c.Repo, doc.Branch)
	if err != nil {
		return handoff.CandidatePR{}, err
	}
	var pr github.PullRequest
	if len(existing) > 0 {
		pr = existing[0]
	} else {
		title, err := c.title(ctx, issue)
		if err != nil {
			return handoff.CandidatePR{}, err
		}
		if pr, err = c.GitHub.CreatePullRequest(ctx, c.Repo, github.NewPullRequest{
			Title: title, Body: prBody(issue, run, attempt, scope, qa), HeadRef: doc.Branch, BaseRef: c.Base,
		}); err != nil {
			return handoff.CandidatePR{}, err
		}
	}
	record := handoff.CandidatePR{
		IssueID: issue, RunID: run, Attempt: attempt, Branch: doc.Branch, Base: c.Base, Number: pr.Number, URL: pr.URL,
		Commit: qa.Candidate.CommitSHA, State: handoff.LandingOpen, OpenedAt: c.Clock.Now().UTC(),
	}
	return record, c.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		return handoff.PutCandidatePR(ctx, tx, record)
	})
}

// scopeOf is the Scope of Work digest the run was briefed from, "" when the
// dispatcher's record of the run cannot say.
func scopeOf(ctx context.Context, kv state.KV, run core.RunID) core.Digest {
	entry, err := kv.GetKV(ctx, handoff.Namespace, "run/"+string(run))
	if err != nil {
		return ""
	}
	var record dispatcher.RunRecord
	if json.Unmarshal(entry.Value, &record) != nil || record.RunID != run {
		return ""
	}
	return record.Brief.ScopeDigest
}

// title is the issue's title, cut to one plain line, with the issue named.
func (c *CandidatePRs) title(ctx context.Context, issue core.IssueRef) (string, error) {
	gh, err := c.GitHub.GetIssue(ctx, issue)
	if err != nil {
		return "", err
	}
	t := strings.Join(strings.FieldsFunc(gh.Title, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	t = strings.NewReplacer("<!--", "", "-->", "").Replace(t)
	if runes := []rune(t); len(runes) > 100 {
		t = string(runes[:100])
	}
	if t == "" {
		t = "Candidate"
	}
	return fmt.Sprintf("%s (#%d)", t, issue.Number), nil
}

// prBody links the issue without closing it (the factory closes the issue
// itself once the release is verified) and names what the candidate was
// judged by. It holds no text of the issue or the agent.
func prBody(issue core.IssueRef, run core.RunID, attempt int, scope core.Digest, qa QAResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Candidate of run `%s` for #%d, built and judged by the factory.\n\nRefs #%d\n\n", run, issue.Number, issue.Number)
	if scope != "" {
		fmt.Fprintf(&b, "- Scope of Work: `%s`\n", scope)
	}
	fmt.Fprintf(&b, "- QA receipt: `%s:%s`, signed by `%s`, score %d\n", Namespace, QAResultKey(run, attempt), qa.Receipt.Signature.KeyID, qa.Receipt.Score)
	fmt.Fprintf(&b, "- Candidate: commit `%s`, build `%s`\n\n", qa.Candidate.CommitSHA, qa.Candidate.Digest)
	b.WriteString("The factory merges this pull request itself, at exactly this commit, once the release built from it is verified in production. " +
		"It never merges a candidate whose release was rolled back, or one that changes foundation or policy files.\n")
	return b.String()
}
