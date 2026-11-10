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
	// runDeadline bounds the run from the opened issue to its closing.
	runDeadline = 30 * time.Minute
	// runPollInterval is the pause between two looks at the run.
	runPollInterval = 15 * time.Second
)

// endStates are the states in which the run has failed for good.
var endStates = map[string]bool{"RolledBack": true, "Blocked": true}

// waitForClose polls until the factory closes the issue, the run ends in a
// failure state, `sisf up` dies, or the deadline passes.
func waitForClose(ctx context.Context, r *commandRunner, o *factoryObserver, proc *factoryProcess, issue string, number int) error {
	ctx, cancel := context.WithTimeout(ctx, runDeadline)
	defer cancel()
	tick := time.NewTicker(runPollInterval)
	defer tick.Stop()
	last, approvalsShown := "", false
	for {
		if !proc.running() {
			return fmt.Errorf("sisf up exited during the run")
		}
		if v, err := o.inspect(ctx, issue); err == nil {
			rec := v.Output.Record
			r.report.RunIDs = addUnique(r.report.RunIDs, rec.RunID)
			if v.Output.Run != nil {
				for _, pr := range v.Output.Run.PullRequests {
					r.report.PullRequests = addUnique(r.report.PullRequests, pr)
				}
			}
			if rec.State != last {
				r.logf("%s is %s", issue, rec.State)
				last = rec.State
			}
			if endStates[rec.State] {
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
		if state, err := issueState(ctx, r, number); err == nil && strings.HasPrefix(state, "CLOSED") {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the issue was not closed within %s; last factory state %q", runDeadline, last)
		case <-tick.C:
		}
	}
}

// assertOutcome checks what the plan requires of a finished run and
// records each result, pass or fail, in the report.
func assertOutcome(ctx context.Context, r *commandRunner, o *factoryObserver, login, issue string, number int, start time.Time) {
	rep := r.report
	journal, err := o.journal(ctx, issue)
	if err != nil {
		r.errorf("the journal of %s cannot be read: %v", issue, err)
	}
	rep.States = journal
	v, inspectErr := o.inspect(ctx, issue)
	rec := v.Output.Record
	rep.RunIDs = addUnique(rep.RunIDs, rec.RunID)

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
	for _, pr := range rep.PullRequests {
		ok, state, err := pullRequestLinks(ctx, r, pr, number)
		linkEvidence = fmt.Sprintf("%s state %s links #%d: %t, error %v", pr, state, number, ok, err)
		if ok && err == nil {
			linked = true
			break
		}
	}
	result(r, "pull request linked to the issue", linked, linkEvidence)

	rep.Receipt = fmt.Sprintf("candidate digest %s, release digest %s; the signature was not verified: "+
		"no sisf command or REST route verifies a QA receipt", orNone(rec.CandidateDigest), orNone(rec.ReleaseDigest))
	rep.check("QA receipt verifies", statusNotChecked, "no CLI or route verifies a receipt; recorded the digests only")

	status, body, err := httpGet(ctx, loopbackURL(install.PortEdge, "/version"))
	result(r, "GET /version served", err == nil && status == http.StatusOK && strings.TrimSpace(body) != "",
		fmt.Sprintf("HTTP %d, body %q, error %v", status, truncate(body, 200), err))
	if rec.CandidateDigest != "" && !strings.Contains(body, rec.CandidateDigest) {
		rep.deviate("the /version body does not name the candidate digest %s (informational)", rec.CandidateDigest)
	}

	b, err := o.budget(ctx)
	rep.Cost = b.summary()
	if err != nil {
		rep.Cost = fmt.Sprintf("budget.get failed: %v", err)
	}
	result(r, "usage ledger non-empty", err == nil && b.spent() > 0, rep.Cost)
	if err == nil && b.Output.Mode != "capped" {
		rep.deviate("the budget mode is %q, not capped", b.Output.Mode)
	}

	outside, err := activityOutsideSandbox(ctx, r, login, start)
	result(r, "nothing outside the sandbox touched", err == nil && len(outside) == 0,
		fmt.Sprintf("events naming another repository since the start: %q, error %v", outside, err))
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
