//go:build smoke

package smoke

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/install"
)

// What the rehearsal of a bad candidate must end in, and what a rehearsal
// of a good one must still do after it closed: both are read from the
// running system and from the supervisor's own records.

// recoveryNamespace and recoveryKeyPrefix are where the release pipeline
// keeps the recovery report of a release it rolled back
// (internal/supervisor/release/restore_and_recovery_report.go).
const (
	recoveryNamespace = "release"
	recoveryKeyPrefix = "recovery/"
)

// recoveryRecord is the part of a recovery report the run reads.
type recoveryRecord struct {
	Release    string `json:"release"`
	RestoredTo string `json:"restored_to"`
	Reason     string `json:"reason"`
	Observed   struct {
		Digest  string `json:"digest"`
		Healthy bool   `json:"healthy"`
	} `json:"observed"`
	DataUntouched bool      `json:"data_untouched"`
	At            time.Time `json:"at"`
}

// rolledBackRelease finds, among the release records of one run, the one
// the pipeline rolled back, and returns its reason.
func rolledBackRelease(records map[string][]byte) (bool, string, string) {
	keys := sortedKeys(records)
	if len(keys) == 0 {
		return false, "", "the supervisor recorded no release for the run"
	}
	var seen []string
	for _, key := range keys {
		var r releaseRecord
		if err := json.Unmarshal(records[key], &r); err != nil {
			seen = append(seen, key+": unreadable")
			continue
		}
		seen = append(seen, describeReleaseRecord(key, r))
		if r.Result == "blocked" && r.State == "Blocked" && r.Reason != "" {
			return true, r.Reason, strings.Join(seen, "; ")
		}
	}
	return false, "", strings.Join(seen, "; ")
}

// issueCommentWith returns the start of the first comment on the issue
// that holds text, "" when none does.
func issueCommentWith(ctx context.Context, r *commandRunner, number int, text string) (string, error) {
	out, err := r.run(ctx, ghLimit, nil, "gh", "api", "--paginate",
		fmt.Sprintf("repos/%s/issues/%d/comments?per_page=100", sandboxRepo, number), "--jq", `.[] | .body | @json`)
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(out, "\n") {
		var body string
		if json.Unmarshal([]byte(line), &body) == nil && strings.Contains(body, text) {
			return truncate(strings.Join(strings.Fields(body), " "), 240), nil
		}
	}
	return "", nil
}

// assertRolledBack checks the end of a rehearsal whose candidate is bad:
// it was built, passed QA, was deployed as its own program and seen
// failing, and the factory then restored the last known good release,
// left the pull request unmerged and blocked the issue with the reason.
func assertRolledBack(ctx context.Context, r *commandRunner, o *factoryObserver, w *candidateWatch, login, issue string, number int, start time.Time) {
	rep := r.report
	journal, err := o.journal(ctx, issue)
	if err != nil {
		r.errorf("the journal of %s cannot be read: %v", issue, err)
	}
	rep.States = journal
	v, inspectErr := o.inspect(ctx, issue)
	rec := v.Output.Record
	rep.RunIDs = addUnique(rep.RunIDs, rec.RunID)
	candidatePull := ""
	if pr := v.Output.PullRequest; inspectErr == nil && pr != nil && pr.URL != "" {
		candidatePull = pr.URL
		rep.PullRequests = addUnique(rep.PullRequests, pr.URL)
	}

	qaRecords, err := o.supervisorRecords(ctx, qaKeyPrefix+rec.RunID+"/")
	passed, qaEvidence := passingReceipt(qaRecords)
	result(r, fmt.Sprintf("the bad candidate was built and passed QA with at least %d", passScore), err == nil && rec.RunID != "" && passed,
		fmt.Sprintf("run %q: %s, error %v", rec.RunID, qaEvidence, err))
	rep.Receipt = fmt.Sprintf("candidate digest %s, release digest %s; %s", orNone(rec.CandidateDigest), orNone(rec.ReleaseDigest), r.scrub.clean(qaEvidence))

	assertCandidateServed(r, w, rec.CandidateDigest, http.StatusInternalServerError, "injected failure")

	releases, err := o.supervisorRecords(ctx, releaseKeyPrefix+rec.RunID+"/")
	rolledBack, reason, releaseEvidence := rolledBackRelease(releases)
	visited := visitedInOrder(journal, "QAEvaluation", "ReleaseCandidate", "Canary", "RolledBack", "Blocked")
	result(r, "release rolled back by the synthetic suite", err == nil && rolledBack && visited && strings.Contains(reason, "synthetic"),
		fmt.Sprintf("%s; journal through QAEvaluation, ReleaseCandidate, Canary, RolledBack and Blocked: %t; error %v", releaseEvidence, visited, err))

	var recovery recoveryRecord
	reports, err := o.records(ctx, recoveryNamespace, recoveryKeyPrefix+rec.RunID)
	raw := reports[recoveryKeyPrefix+rec.RunID]
	decodeErr := json.Unmarshal(raw, &recovery)
	restored := err == nil && decodeErr == nil && recovery.Release == rec.CandidateDigest && recovery.RestoredTo == w.installed &&
		recovery.Observed.Digest == w.installed && recovery.Observed.Healthy
	result(r, "recovery report restores the last known good release", restored,
		fmt.Sprintf("release %s restored to %s at %s, observed %s healthy %t, data untouched %t, reason %q; the installed release is %s; errors %v, %v",
			orNone(recovery.Release), orNone(recovery.RestoredTo), recovery.At.UTC().Format(time.RFC3339), orNone(recovery.Observed.Digest),
			recovery.Observed.Healthy, recovery.DataUntouched, truncate(recovery.Reason, 200), w.installed, err, decodeErr))

	revision, revErr := edgeRevision(ctx)
	status, page, pageErr := httpGet(ctx, loopbackURL(install.PortEdge, "/"))
	result(r, "the last known good release serves through the edge again", revErr == nil && pageErr == nil && revision == w.installed &&
		status == http.StatusOK && strings.Contains(page, "visit"),
		fmt.Sprintf("the edge serves revision %s (installed %s); GET / answered HTTP %d %q; errors %v, %v",
			orNone(revision), w.installed, status, truncate(strings.TrimSpace(page), 120), revErr, pageErr))

	unmerged, pullEvidence := false, "the run recorded no pull request of the candidate"
	if candidatePull != "" {
		pr, err := pullRequestMerge(ctx, r, candidatePull)
		unmerged = err == nil && pr.State == "OPEN" && pr.MergeCommit == ""
		pullEvidence = fmt.Sprintf("%s state %q, merge commit %q, error %v", candidatePull, pr.State, pr.MergeCommit, err)
	}
	result(r, "pull request left unmerged", unmerged, pullEvidence)

	ghState, ghErr := issueState(ctx, r, number)
	lastJournal := ""
	if len(journal) > 0 {
		lastJournal = journal[len(journal)-1].State
	}
	comment, commentErr := "", error(nil)
	if reason != "" {
		comment, commentErr = issueCommentWith(ctx, r, number, reason)
	}
	result(r, "issue left Blocked with the reason", ghErr == nil && strings.HasPrefix(ghState, "OPEN") && rec.State == "Blocked" &&
		lastJournal == "Blocked" && commentErr == nil && comment != "",
		fmt.Sprintf("GitHub %q, factory record %q, last journal state %q, inspect error %v; the reason %q on the issue: %q, error %v",
			ghState, rec.State, lastJournal, inspectErr, truncate(reason, 200), comment, commentErr))

	gone, storeEvidence := programRemoved(ctx, o.home, rec.CandidateDigest)
	result(r, "the rolled-back program is removed from the supervisor's store", gone, storeEvidence)

	paid, evidence := paidAgentConfigured(o.home)
	result(r, "no paid model configured", !paid, evidence)
	b, err := o.budget(ctx)
	rep.Cost = b.summary()
	if err != nil {
		rep.Cost = fmt.Sprintf("budget.get failed: %v", err)
	}
	assertSandboxScope(ctx, r, o, login, start)
}

// programRemoved waits a short while for the supervisor to remove the
// program of digest from its store, which it does after the round that
// ended the release.
func programRemoved(ctx context.Context, home, digest string) (bool, string) {
	path := storedProgram(home, digest)
	if digest == "" {
		return false, "the factory records no candidate digest"
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		_, err := os.Stat(path)
		if os.IsNotExist(err) {
			entries, _ := os.ReadDir(programStoreDir(home))
			return true, fmt.Sprintf("the store has no file for %s; it holds %d file(s)", digest, len(entries))
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false, fmt.Sprintf("the store still holds the file for %s (stat error %v)", digest, err)
		}
		time.Sleep(pollInterval)
	}
}

// assertPromotedSurvivesRestarts checks that the factory keeps serving the
// program it promoted: after every unit is restarted in place, which gives
// the supervisor a new process with nothing in memory, and after `sisf
// down` and a new `sisf up`, which rebuilds and re-signs the install. It
// returns the `sisf up` that runs at the end.
func assertPromotedSurvivesRestarts(ctx context.Context, r *commandRunner, proc *factoryProcess, sisf, home, upLog string, env []string, digest, greeting string) *factoryProcess {
	check := func(name string, before []appListener, extra string) {
		ok, evidence := servesPromoted(ctx, r, home, digest, before, 5*time.Minute)
		status, page, err := httpGet(ctx, loopbackURL(install.PortEdge, "/"))
		ok = ok && err == nil && status == http.StatusOK && strings.Contains(page, greeting)
		result(r, name, ok, evidence+extra)
	}
	before := appListeners(ctx, r)
	if _, err := r.run(ctx, 2*time.Minute, env, sisf, "restart", "--dir", home); err != nil {
		result(r, "the promoted program serves after a restart of the units", false, "sisf restart failed: "+err.Error())
		return proc
	}
	check("the promoted program serves after a restart of the units", before, "")

	before = appListeners(ctx, r)
	stopped := proc.stop(r, sisf, home, env)
	next, err := startFactory(r, sisf, upArgs(home, repoRoot()), env, upLog)
	if err != nil {
		result(r, "the promoted program serves after sisf down and sisf up", false, "sisf up could not be started again: "+err.Error())
		return proc
	}
	if err := next.waitUntilUp(ctx); err != nil {
		result(r, "the promoted program serves after sisf down and sisf up", false, err.Error())
		return next
	}
	up, _ := os.ReadFile(upLog)
	dropped := strings.Contains(string(up), "is dropped")
	check("the promoted program serves after sisf down and sisf up", before,
		"; "+stopped+"; the new sisf up dropped the promotion: "+strconv.FormatBool(dropped))
	if dropped {
		r.errorf("the new sisf up dropped the promoted release:\n%s", lastLines(string(up), 20))
	}
	return next
}

// closeRehearsalWork closes what a rehearsal left open in the sandbox: its
// issue, when the factory did not close it, and its pull requests, when
// they were not merged. Branches stay.
func closeRehearsalWork(r *commandRunner, number int, pullRequests []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var notes []string
	if state, err := issueState(ctx, r, number); err == nil && strings.HasPrefix(state, "OPEN") {
		_, err := r.run(ctx, ghLimit, nil, "gh", "issue", "close", strconv.Itoa(number), "--repo", sandboxRepo, "--reason", "not planned",
			"--comment", "Closed by the release rehearsal so the sandbox keeps no open factory work.")
		notes = append(notes, fmt.Sprintf("issue #%d closed (error %v)", number, err))
	}
	for _, url := range pullRequests {
		pr, err := pullRequestMerge(ctx, r, url)
		if err != nil || pr.State != "OPEN" {
			continue
		}
		_, err = r.run(ctx, ghLimit, nil, "gh", "pr", "close", url,
			"--comment", "Closed by the release rehearsal so the sandbox keeps no open factory work; the branch is kept.")
		notes = append(notes, fmt.Sprintf("%s closed (error %v)", url, err))
	}
	if len(notes) == 0 {
		notes = []string{"the rehearsal left no issue or pull request of its own open"}
	}
	r.report.check("rehearsal's own issue and pull requests closed", statusInfo, r.scrub.clean(strings.Join(notes, "; ")))
}
