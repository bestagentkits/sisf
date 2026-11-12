//go:build smoke

package smoke

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/install"
)

const (
	// runDeadline bounds the run from the opened issue to its closing. The
	// release keeps the real defaults: canary steps of one 10-minute window
	// each, two observation windows and, at the sandbox's low traffic, the
	// 30-minute synthetic hold; the coding and judging come on top.
	runDeadline = 100 * time.Minute
	// runPollInterval is the pause between two looks at the run.
	runPollInterval = 15 * time.Second
)

// endStates are the states in which the run has failed for good.
var endStates = map[string]bool{"RolledBack": true, "Blocked": true}

// waitForClose polls until the factory closes the issue, the run ends in a
// failure state, `sisf up` or one of its units dies, the factory's records
// show the run cannot finish (see factoryCannotFinish), QA held the
// candidate or the run stalled in a state (see qaStopped), or the deadline
// passes. A failure ends the wait at once; the test's cleanup adds the
// unit log tails to the report.
//
// Along the way watch looks for the candidate on the edge. With wantBlocked
// the run is the rehearsal of a rollback: Blocked is then the end it waits
// for, and a closed issue the failure.
func waitForClose(ctx context.Context, r *commandRunner, o *factoryObserver, proc *factoryProcess, watch *candidateWatch, issue string, number int, wantBlocked bool) error {
	ctx, cancel := context.WithTimeout(ctx, runDeadline)
	defer cancel()
	tick := time.NewTicker(runPollInterval)
	defer tick.Stop()
	last, approvalsShown := "", false
	for {
		if !proc.running() {
			return fmt.Errorf("sisf up exited during the run")
		}
		runStatus, runID := "", ""
		if v, err := o.inspect(ctx, issue); err == nil {
			rec := v.Output.Record
			r.report.RunIDs = addUnique(r.report.RunIDs, rec.RunID)
			runID = rec.RunID
			if v.Output.Run != nil {
				runStatus = v.Output.Run.Status
				for _, pr := range v.Output.Run.PullRequests {
					r.report.PullRequests = addUnique(r.report.PullRequests, pr)
				}
			}
			if pr := v.Output.PullRequest; pr != nil && pr.URL != "" {
				r.report.PullRequests = addUnique(r.report.PullRequests, pr.URL)
			}
			if rec.State != last {
				r.logf("%s is %s", issue, rec.State)
				last = rec.State
			}
			pull := ""
			if pr := v.Output.PullRequest; pr != nil {
				pull = pr.URL
			}
			watch.look(ctx, r, rec.CandidateDigest, pull)
			switch {
			case wantBlocked && rec.State == "Blocked":
				return nil
			case wantBlocked && rec.State == "RolledBack":
				// On its way to Blocked, once the recovery report is written.
			case endStates[rec.State]:
				return fmt.Errorf("the run ended in %s", rec.State)
			}
			if rec.State == "ApprovalPending" && !approvalsShown {
				out, _ := o.approvals(ctx)
				r.logf("%s waits for an Owner decision; decide it with `sisf approvals decide` before the deadline. Requests:\n%s", issue, out)
				approvalsShown = true
			}
		} else {
			r.logf("runs.inspect %s: %v", issue, err) // not tracked yet, or the factory is busy
		}
		reason := factoryCannotFinish(o, proc.log, runStatus)
		if reason == "" {
			reason = qaStopped(ctx, o, issue, runID, time.Now())
		}
		if reason != "" {
			return fmt.Errorf("the factory cannot finish the run, last state %q: %s", last, reason)
		}
		if state, err := issueState(ctx, r, number); err == nil && strings.HasPrefix(state, "CLOSED") {
			if wantBlocked {
				return fmt.Errorf("the issue was closed, but the release of this candidate had to be rolled back; last factory state %q", last)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the run did not end within %s; last factory state %q", runDeadline, last)
		case <-tick.C:
		}
	}
}

// assertOutcome checks what the plan requires of a finished run and
// records each result, pass or fail, in the report. A rehearsal, when
// given, is checked for its greeting instead of the paid run's route, and for
// running no paid model instead of a non-empty usage ledger.
func assertOutcome(ctx context.Context, r *commandRunner, o *factoryObserver, watch *candidateWatch, login, issue string, number int, start time.Time, reh *rehearsal) {
	rep := r.report
	journal, err := o.journal(ctx, issue)
	if err != nil {
		r.errorf("the journal of %s cannot be read: %v", issue, err)
	}
	rep.States = journal
	v, inspectErr := o.inspect(ctx, issue)
	rec := v.Output.Record
	rep.RunIDs = addUnique(rep.RunIDs, rec.RunID)
	if pr := v.Output.PullRequest; inspectErr == nil && pr != nil && pr.URL != "" {
		rep.PullRequests = addUnique(rep.PullRequests, pr.URL) // a poll during the run may have missed it
	}

	ghState, ghErr := issueState(ctx, r, number)
	lastJournal := ""
	if len(journal) > 0 {
		lastJournal = journal[len(journal)-1].State
	}
	evidence := fmt.Sprintf("GitHub %q, factory record %q, last journal state %q, inspect error %v", ghState, rec.State, lastJournal, inspectErr)
	result(r, "issue closed by the factory", ghErr == nil && strings.HasPrefix(ghState, "CLOSED") && rec.State == "Closed" && lastJournal == "Closed", evidence)

	n, err := progressComments(ctx, r, number)
	result(r, "exactly one progress comment", err == nil && n == 1, fmt.Sprintf("%d progress comments, error %v", n, err))

	linked, linkEvidence := false, "the run recorded no pull request"
	var merged mergedPullRequest
	mergeEvidence := "the run recorded no pull request"
	for _, pr := range rep.PullRequests {
		ok, state, err := pullRequestLinks(ctx, r, pr, number)
		if ok && err == nil && !linked {
			linked = true
			linkEvidence = fmt.Sprintf("%s state %s links #%d", pr, state, number)
		} else if !linked {
			linkEvidence = fmt.Sprintf("%s state %s links #%d: %t, error %v", pr, state, number, ok, err)
		}
		m, err := pullRequestMerge(ctx, r, pr)
		mergeEvidence = fmt.Sprintf("%s state %q, merge commit %q, error %v", pr, m.State, m.MergeCommit, err)
		if err == nil && m.State == "MERGED" && m.MergeCommit != "" {
			merged = m
			break
		}
	}
	result(r, "pull request linked to the issue", linked, linkEvidence)
	result(r, "pull request merged in the sandbox", merged.MergeCommit != "", mergeEvidence)

	// The release ran the candidate's own build: the edge served its
	// digest, and the store's file for it has that sha256, while the pull
	// request was still open. A rehearsal knows what its page must say.
	wantPage := ""
	if reh != nil {
		wantPage = reh.greeting()
	}
	assertCandidateServed(r, watch, rec.CandidateDigest, http.StatusOK, wantPage)

	// The proof the change landed on the default branch.
	landed, landEvidence := false, "no merged pull request"
	if merged.MergeCommit != "" {
		ok, evidence, err := false, "", error(nil)
		if reh != nil {
			ok, evidence, err = greetingOnDefaultBranch(ctx, r, merged.MergeCommit, reh.greeting())
		} else {
			ok, evidence, err = changeOnDefaultBranch(ctx, r, merged.MergeCommit, versionRoute)
		}
		landed, landEvidence = ok && err == nil, fmt.Sprintf("%s, error %v", evidence, err)
	}
	result(r, "requested change merged on the default branch", landed, landEvidence)

	qaRecords, err := o.supervisorRecords(ctx, qaKeyPrefix+rec.RunID+"/")
	passed, qaEvidence := passingReceipt(qaRecords)
	result(r, fmt.Sprintf("signed QA receipt scoring at least %d", passScore), err == nil && rec.RunID != "" && passed,
		fmt.Sprintf("run %q: %s, error %v", rec.RunID, qaEvidence, err))
	rep.Receipt = fmt.Sprintf("candidate digest %s, release digest %s; %s", orNone(rec.CandidateDigest), orNone(rec.ReleaseDigest), r.scrub.clean(qaEvidence))
	rep.check("QA receipt signature verifies", statusNotChecked,
		"no CLI or route verifies a receipt; the run checked the supervisor's record of a signed receipt and that the issue passed the QA guard into ReleaseCandidate")

	releases, err := o.supervisorRecords(ctx, releaseKeyPrefix+rec.RunID+"/")
	released, releaseEvidence := completedRelease(releases)
	visited := visitedInOrder(journal, "QAEvaluation", "ReleaseCandidate", "Verified", "Closed")
	result(r, "release completed", err == nil && rec.RunID != "" && released && visited,
		fmt.Sprintf("%s; journal through QAEvaluation, ReleaseCandidate, Verified and Closed: %t; error %v", releaseEvidence, visited, err))

	status, body, err := httpGet(ctx, loopbackURL(install.PortEdge, "/healthz"))
	result(r, "application healthy through the edge", err == nil && status == http.StatusOK,
		fmt.Sprintf("GET /healthz on the edge: HTTP %d, body %q, error %v", status, truncate(body, 200), err))

	b, err := o.budget(ctx)
	rep.Cost = b.summary()
	if err != nil {
		rep.Cost = fmt.Sprintf("budget.get failed: %v", err)
	}
	if reh == nil {
		result(r, "usage ledger non-empty", err == nil && b.spent() > 0, rep.Cost)
	} else {
		paid, evidence := paidAgentConfigured(o.home)
		result(r, "no paid model configured", !paid, evidence)
	}
	if err == nil && b.Output.Mode != "capped" {
		rep.deviate("the budget mode is %q, not capped", b.Output.Mode)
	}

	assertSandboxScope(ctx, r, o, login, start)
}

// result records a check and fails the test when it did not pass.
func result(r *commandRunner, name string, ok bool, evidence string) {
	r.t.Helper()
	evidence = r.scrub.clean(evidence)
	if ok {
		r.report.check(name, statusPass, evidence)
		return
	}
	r.report.check(name, statusFail, evidence)
	r.errorf("%s: %s", name, evidence)
}

func httpGet(ctx context.Context, url string) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, string(raw), err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
