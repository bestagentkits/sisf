package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/telemetry"
)

// Bounds of the publication of stored reports.
const (
	// DefaultReportPublishBatch bounds the issues one Publish opens, so a
	// full store drains over many passes and never bursts GitHub; the
	// daily budget (ReportDailyPublishBudget) bounds them over a day.
	DefaultReportPublishBatch = 5
	// maxReportRejections is how many times GitHub may refuse a report's
	// issue as invalid before the report is given up: one refusal may be
	// GitHub's passing state, a repeated one is about the report.
	maxReportRejections = 3
	// reportLookupSlack is how long before an unanswered attempt the
	// lookup of the issue it may have opened starts, for clock skew.
	reportLookupSlack = time.Minute
	// reportLookupPages bounds the pages of 100 issues that lookup reads.
	reportLookupPages = 10
	// reportAttemptTimeout bounds one attempt to open a report's issue, so
	// an attempt older than it and reportLookupSlack is over at any
	// publisher sharing the store, and one that found no issue opened
	// nothing.
	reportAttemptTimeout = 2 * time.Minute
	// reportAttemptOver is how long after it started an attempt is surely
	// over at every publisher sharing the store.
	reportAttemptOver = reportAttemptTimeout + reportLookupSlack
	// reportMaxRetryWait bounds the wait between two attempts of one
	// report whose attempts go unanswered, as the publication pass bounds
	// its own wait after a failure: the wait doubles from
	// reportAttemptOver up to it, so a long outage is not met with an
	// attempt of each report every few minutes.
	reportMaxRetryWait = 5 * time.Minute
)

// reportIDForm is the form the report endpoint gives report IDs.
var reportIDForm = regexp.MustCompile(`^report-[0-9a-f]{16}$`)

// ReportIssues is what publishing reports does on GitHub: open an issue,
// label one GitHub opened without its label, and list the issues changed
// since a time, to find one an attempt whose answer was lost may have
// opened. github.RESTClient is one.
type ReportIssues interface {
	CreateIssue(ctx context.Context, repo github.RepoRef, n github.NewIssue) (github.Issue, error)
	AddLabels(ctx context.Context, ref core.IssueRef, names ...string) error
	PollIssues(ctx context.Context, repo github.RepoRef, opts github.ListIssuesOptions, etag string) (github.IssuePage, error)
}

// ReportPublisher opens one GitHub issue for each report the public report
// route stored in the control zone's store, labelled report.IssueLabel
// only, for a person to triage. A report never starts work by itself.
type ReportPublisher struct {
	// Store is the control zone's operational store.
	Store  state.Store
	Issues ReportIssues
	Repo   github.RepoRef
	// Self is the factory's account; an issue it did not write is never
	// taken for one it opened.
	Self github.Identity
	// Redactor scrubs the text once more before it leaves the factory.
	Redactor telemetry.Redactor
	Clock    core.Clock
	// Batch bounds the issues one Publish opens; zero means
	// DefaultReportPublishBatch.
	Batch int
}

// ReportPublishResult is what one Publish did.
type ReportPublishResult struct {
	// Published counts the reports now shown as issues, Rejected those
	// whose issue GitHub refused this time, and Unlabelled the issues
	// GitHub shows without report.IssueLabel even after the publisher tried
	// to add it, as for a token that may open issues but not label them.
	Published  int
	Rejected   int
	Unlabelled int
	// PausedUntil is when publishing goes on, set once the daily budget of
	// report issues is spent; zero while it is not.
	PausedUntil time.Time
	// labelFailure is why the label of the last unlabelled issue could not
	// be added.
	labelFailure error
	// held counts the reports held back because their last attempt may
	// still be answered (errAttemptInFlight) or, after attempts that went
	// unanswered, their next one is not due yet (errRetryNotDue).
	held int
}

// Publish publishes the reports waiting in the order they were received,
// asking GitHub about at most Batch of them, and no more than the daily
// budget has left (ReportDailyPublishBudget); with the budget spent it
// asks for no report's first issue and says until when publishing pauses,
// keeping every report, but still settles the attempts that went
// unanswered (publishBatch). A report whose last attempt got no answer is looked for among the issues the
// factory's account opened since, by the marker its body opens with and
// with or without the label, so an issue opened just before a crash is
// recorded instead of opened again; only when GitHub lists more than 1,000
// issues changed since may one such report be published twice. An issue
// GitHub shows without the label is given it; when that fails, the
// factory's status says the token may not label issues. GitHub refusing
// the issue of a report, a client error that is not about the token, the
// repository or the rate limit, is counted against that report and the
// next one is tried; after maxReportRejections it is kept apart and not
// tried again. Any other failure, an outage or a refused token, holds for
// every report: it ends the call with that error and leaves the report
// waiting, for the caller to try again after a pause.
//
// An attempt left without an answer stays charged against the budget until
// it is surely over and a lookup found no issue it opened; its charge is
// then taken back, as the next attempt is charged or, when that is not
// due yet, at once. Until then the report
// waits behind the others, and a report whose attempts keep going
// unanswered is tried again after a wait that doubles up to
// reportMaxRetryWait. Such a report is never given up for that: it leaves
// the queue once published, refused by GitHub, past the reports' retention
// or pushed out of a full store.
func (p ReportPublisher) Publish(ctx context.Context) (ReportPublishResult, error) {
	var result ReportPublishResult
	if p.Store == nil || p.Issues == nil || p.Redactor == nil || p.Self.Login == "" {
		return result, core.NewError(core.CodeInvalidArgument, "publishing reports needs the store, an issue client, a redactor and the factory's account by its login")
	}
	if p.Clock == nil {
		p.Clock = core.SystemClock{}
	}
	batch := p.Batch
	if batch <= 0 {
		batch = DefaultReportPublishBatch
	}
	var waiting []state.KVEntry
	var fresh int
	err := p.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		budget, err := readPublishBudget(ctx, tx, p.Clock.Now())
		if err != nil {
			return err
		}
		fresh = budget.remaining()
		// Every waiting report is read, so reports held back while their
		// attempts go unanswered cannot keep those behind them from being
		// tried; the store's capacity bounds them.
		waiting, err = tx.ListKV(ctx, ReportsNamespace, reportPrefix, 0)
		return err
	})
	if err != nil {
		return result, err
	}
	err = p.publishBatch(ctx, waiting, batch, fresh, &result)
	failure := err
	if failure == nil && result.Unlabelled > 0 {
		failure = missingLabelPermission(result.labelFailure)
	}
	// A pass that only held reports back, or that the spent budget kept
	// from asking for any, learned nothing of GitHub, so the last failure
	// stays shown.
	if failure != nil || result.Published > 0 || (result.held == 0 && fresh > 0) {
		if noted := p.noteError(ctx, failure, result.Published > result.Unlabelled); err == nil {
			err = noted
		}
	}
	if err == nil {
		err = p.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
			budget, err := readPublishBudget(ctx, tx, p.Clock.Now())
			result.PausedUntil = budget.pausedUntil()
			return err
		})
	}
	return result, err
}

// publishBatch publishes the reports stored at waiting, oldest first,
// asking GitHub about at most batch of them, and stops at the first
// failure that is not about one report. A report held back asks GitHub
// nothing and is not counted against batch. At most fresh of them are
// reports without an attempt in flight, each of which needs a charge of
// its own; with fresh at zero, as with the budget spent, a report whose
// attempt went unanswered is still looked for, and tried again on the
// charge that attempt holds, so that charge is not kept for a day.
func (p ReportPublisher) publishBatch(ctx context.Context, waiting []state.KVEntry, batch, fresh int, result *ReportPublishResult) error {
	asked := 0
	for _, entry := range waiting {
		if asked >= batch {
			return nil
		}
		var stored storedReport
		if err := json.Unmarshal(entry.Value, &stored); err != nil || !reportIDForm.MatchString(stored.ID) || stored.Fingerprint == "" {
			if err := p.reject(ctx, entry.Key, core.CodeTampered); err != nil && !core.HasCode(err, core.CodeConflict) {
				return err
			}
			continue
		}
		if heldBack(stored.Publish, p.Clock.Now()) {
			result.held++
			continue
		}
		if stored.Publish.AttemptedAt.IsZero() {
			if fresh <= 0 {
				continue
			}
			fresh--
		}
		asked++
		published, labelFailure, refused, err := p.publishOne(ctx, entry, stored)
		switch {
		case refused != nil:
			result.Rejected++
			if err := p.reject(ctx, entry.Key, core.CodeOf(refused)); err != nil && !core.HasCode(err, core.CodeConflict) {
				return err
			}
		case errors.Is(err, errAttemptInFlight), errors.Is(err, errRetryNotDue):
			result.held++
		case errors.Is(err, errBudgetSpent):
			// Another publisher spent what was left of the budget; the
			// reports whose attempts are in flight are still looked for.
			fresh = 0
		case core.HasCode(err, core.CodeConflict):
			// The report or the budget changed meanwhile, a duplicate
			// counted or another publisher's attempt recorded; the next
			// call reads it again.
		case err != nil:
			return err
		case published:
			result.Published++
			if labelFailure != nil {
				result.Unlabelled++
				result.labelFailure = labelFailure
			}
		}
	}
	return nil
}

// attemptOver reports whether an attempt started at attempted is surely
// over at now (reportAttemptOver).
func attemptOver(attempted, now time.Time) bool {
	return !now.Before(attempted.Add(reportAttemptOver))
}

// retryWait is how long after the last of unanswered attempts that went
// unanswered and opened nothing the report is tried again:
// reportAttemptOver after the first, doubling up to reportMaxRetryWait.
func retryWait(unanswered int) time.Duration {
	wait := reportAttemptOver
	for i := 1; i < unanswered && wait < reportMaxRetryWait; i++ {
		wait *= 2
	}
	return min(wait, reportMaxRetryWait)
}

// retryDue reports whether a report whose publication stands at st may be
// asked for again at now, as far as its unanswered attempts go.
func retryDue(st reportPublishState, now time.Time) bool {
	return st.Unanswered == 0 || !now.Before(st.UnansweredAt.Add(retryWait(st.Unanswered)))
}

// heldBack reports whether a report whose publication stands at st waits
// at now without GitHub being asked anything about it: no attempt of it is
// in flight, whose issue is looked for, and its next one is not due.
func heldBack(st reportPublishState, now time.Time) bool {
	return st.AttemptedAt.IsZero() && !retryDue(st, now)
}

// errBudgetSpent ends a batch whose next attempt found the daily budget
// spent, as by another publisher sharing the store.
var errBudgetSpent = errors.New("the daily budget of report issues is spent")

// errAttemptInFlight holds back a report whose last attempt is not surely
// over (reportAttemptTimeout): another publisher may still be waiting for
// GitHub's answer to it. The batch goes on with the reports behind it.
var errAttemptInFlight = errors.New("the report's last attempt may still be waiting for GitHub's answer")

// errRetryNotDue holds back a report whose attempts went unanswered until
// its next attempt is due (retryWait). The batch goes on with the reports
// behind it.
var errRetryNotDue = errors.New("the report's next attempt is not due yet")

// missingLabelPermissionMessage opens what the status says while report
// issues are opened without their label.
const missingLabelPermissionMessage = "report issues are opened without the label " + report.IssueLabel +
	" and it could not be added: the factory's GitHub token may open issues but not label them; give it permission to label issues"

// missingLabelPermission is the failure noted while report issues are
// opened without their label, why adding it failed included.
func missingLabelPermission(cause error) error {
	err := core.NewError(core.CodePermissionDenied, missingLabelPermissionMessage)
	if cause != nil {
		err = err.WithCause(cause)
	}
	return err
}

// reportPublishErrorKey holds why the last publication failed, for the
// factory's status; absent while it succeeds.
const reportPublishErrorKey = "publish-error"

// noteError records why publishing failed, or that it no longer does,
// writing only when that changed. A missing permission to label report
// issues is cleared only by a report issue that carries its label
// (labelled), not by a call that published nothing.
func (p ReportPublisher) noteError(ctx context.Context, failure error, labelled bool) error {
	return p.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, ReportsNamespace, reportPublishErrorKey)
		switch {
		case core.HasCode(err, core.CodeNotFound):
			if failure == nil {
				return nil
			}
			entry = state.KVEntry{Namespace: ReportsNamespace, Key: reportPublishErrorKey}
		case err != nil:
			return err
		case failure == nil && !labelled && strings.Contains(string(entry.Value), missingLabelPermissionMessage):
			return nil
		case failure == nil:
			return tx.DeleteKV(ctx, ReportsNamespace, entry.Key, entry.Version)
		}
		message := string(core.CodeOf(failure)) + ": " + failure.Error()
		if string(entry.Value) == message {
			return nil
		}
		entry.Value = []byte(message)
		_, err = tx.PutKV(ctx, entry)
		return err
	})
}

// publishOne publishes the report stored at entry, read as stored, and
// reports whether it is now shown as an issue, and, when that issue does
// not carry the label and it could not be added, why (labelFailure).
// GitHub's refusal of the report's issue is returned as refused, apart
// from the failures that hold for every report. A report whose last
// attempt is over and opened nothing, but whose next one is not due yet,
// has that attempt's charge taken back and is held back (errRetryNotDue).
func (p ReportPublisher) publishOne(ctx context.Context, entry state.KVEntry, stored storedReport) (published bool, labelFailure, refused, err error) {
	attempt := stored
	previous := stored.Publish.AttemptedAt
	if !previous.IsZero() {
		issue, found, err := p.findIssue(ctx, stored)
		if err != nil {
			return false, nil, nil, err
		}
		if found {
			labelFailure, err := p.recordIssue(ctx, entry.Key, stored, issue)
			return true, labelFailure, nil, err
		}
		// An earlier attempt that is not surely over may still be waiting
		// for GitHub's answer at another publisher; trying again now could
		// open a second issue that only one charge pays for.
		if !attemptOver(previous, p.Clock.Now()) {
			return false, nil, nil, errAttemptInFlight
		}
		// The earlier attempt went unanswered and opened no issue.
		attempt.Publish.AttemptedAt = time.Time{}
		attempt.Publish.Unanswered++
		attempt.Publish.UnansweredAt = previous
		if !retryDue(attempt.Publish, p.Clock.Now()) {
			if err := p.settle(ctx, entry, previous, attempt); err != nil {
				return false, nil, nil, err
			}
			return false, nil, nil, errRetryNotDue
		}
	}
	settled := attempt
	attempt.Publish.AttemptedAt = p.Clock.Now().UTC()
	spent := false
	if err := p.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		room, err := reserveAttempt(ctx, tx, previous, attempt.Publish.AttemptedAt)
		switch {
		case err != nil:
			return err
		case !room && !previous.IsZero():
			// The earlier attempt holds no charge any more, so nothing
			// made room; it is over all the same.
			spent = true
			return putStored(ctx, tx, entry.Key, entry.Version, settled)
		case !room:
			spent = true
			return nil
		}
		return putStored(ctx, tx, entry.Key, entry.Version, attempt)
	}); err != nil {
		return false, nil, nil, err
	}
	if spent {
		return false, nil, nil, errBudgetSpent
	}
	// The text is made visible and redacted again, as the route does, in
	// case it was stored by an older route, and redacted once more after
	// it is defused for GitHub.
	shown := stored.Report
	shown.Title = p.Redactor.Redact(report.Visible(shown.Title))
	shown.Body = p.Redactor.Redact(report.Visible(shown.Body))
	createCtx, cancel := context.WithTimeout(ctx, reportAttemptTimeout)
	defer cancel()
	issue, err := p.Issues.CreateIssue(createCtx, p.Repo, github.NewIssue{
		Title: reportIssueTitle(shown, p.Redactor.Redact), Body: reportIssueBody(shown, p.Redactor.Redact), Labels: []string{report.IssueLabel}})
	switch {
	case refusesReport(err):
		return false, nil, err, nil
	case answeredWithClientError(err):
		// GitHub answered, so no issue was opened: the attempt's charge
		// is given back and the attempt is no longer one without an
		// answer. An attempt left without an answer, or answered with an
		// outage, stays charged, since it may have opened one.
		if released := p.answered(ctx, entry.Key, attempt.Publish.AttemptedAt); released != nil {
			return false, nil, nil, errors.Join(err, released)
		}
		return false, nil, nil, err
	case err != nil:
		return false, nil, nil, err
	}
	labelFailure, err = p.recordIssue(ctx, entry.Key, stored, issue)
	return true, labelFailure, nil, err
}

// settle records that the attempt made at previous for the report stored
// at entry is over and opened nothing, as a lookup showed: its charge is
// taken back and the report stored as attempt says, in one write, unless
// the report changed meanwhile, which is a conflict.
func (p ReportPublisher) settle(ctx context.Context, entry state.KVEntry, previous time.Time, attempt storedReport) error {
	return p.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		if err := releaseAttempt(ctx, tx, previous, p.Clock.Now()); err != nil {
			return err
		}
		return putStored(ctx, tx, entry.Key, entry.Version, attempt)
	})
}

// recordIssue records the report stored at key as shown by issue and then,
// when GitHub shows the issue without report.IssueLabel, adds it, returning
// why when that failed. The issue is recorded first, so a failure to label
// it never opens it again.
func (p ReportPublisher) recordIssue(ctx context.Context, key string, stored storedReport, issue github.Issue) (labelFailure, err error) {
	if err := p.markPublished(ctx, key, stored, issue.Ref.Number); err != nil {
		return nil, err
	}
	if report.HasIssueLabel(issue) {
		return nil, nil
	}
	if err := p.Issues.AddLabels(ctx, issue.Ref, report.IssueLabel); err != nil {
		return err, nil
	}
	return nil, nil
}

// findIssue looks for the issue an unanswered attempt to publish r may have
// opened: an issue the factory's account wrote whose body opens with r's
// marker. Only the publisher writes that first line, so a report's text,
// which may name any ID, cannot pass for it. The report issues changed
// since the attempt are looked at first; a token that may open issues but
// not label them opens them without the label, so then every issue
// changed since is. Each listing reads at most reportLookupPages pages of
// 100 issues: only when more than 1,000 issues changed since the attempt
// may the issue be missed and opened again.
func (p ReportPublisher) findIssue(ctx context.Context, r storedReport) (github.Issue, bool, error) {
	since := r.Publish.AttemptedAt.Add(-reportLookupSlack)
	marker := report.IssueMarker(r.ID) + "\n"
	for _, labels := range [][]string{{report.IssueLabel}, nil} {
		page, err := p.Issues.PollIssues(ctx, p.Repo, github.ListIssuesOptions{Labels: labels, Since: since, MaxPages: reportLookupPages}, "")
		if err != nil {
			return github.Issue{}, false, err
		}
		for _, issue := range page.Issues {
			if p.Self.Wrote(issue.Author) && strings.HasPrefix(issue.Body, marker) {
				return issue, true, nil
			}
		}
	}
	return github.Issue{}, false, nil
}

// markPublished replaces the report stored at key with what answers a
// duplicate of it, the issue it is shown as included, in one write; the
// issue was charged against the daily budget when it was asked for. The
// report may have been counted again, pruned or pushed out meanwhile; what
// answers a duplicate is recorded all the same.
func (p ReportPublisher) markPublished(ctx context.Context, key string, r storedReport, number int) error {
	return p.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		count, err := readCounter(ctx, tx, reportCountKey)
		if err != nil {
			return err
		}
		kept := publishedReport{ID: r.ID, Fingerprint: r.Fingerprint, Issue: number,
			ReceivedAt: r.ReceivedAt, LastSeenAt: r.LastSeenAt, Count: r.Count}
		current, err := tx.GetKV(ctx, ReportsNamespace, key)
		switch {
		case err == nil:
			var now storedReport
			if json.Unmarshal(current.Value, &now) == nil {
				kept.LastSeenAt, kept.Count = now.LastSeenAt, now.Count
			}
			if err := tx.DeleteKV(ctx, ReportsNamespace, key, current.Version); err != nil {
				return err
			}
			if err := forgetWaiting(ctx, tx, r.Client, key); err != nil {
				return err
			}
			count.n = max(count.n-1, 0)
			if err := putCount(ctx, tx, count); err != nil {
				return err
			}
		case !core.HasCode(err, core.CodeNotFound):
			return err
		}
		keptKey := reportKey(reportPublishPrefix, r.ReceivedAt, r.Fingerprint)
		if err := putJSON(ctx, tx, keptKey, 0, kept); err != nil {
			return err
		}
		return putIndex(ctx, tx, r.Fingerprint, keptKey)
	})
}

// answered records that GitHub answered the attempt made at attempted for
// the report stored at key without opening an issue: the attempt's charge
// is taken back, the report no longer waits for its answer, and, GitHub
// answering again, its next attempt is not put off for those that went
// unanswered before.
func (p ReportPublisher) answered(ctx context.Context, key string, attempted time.Time) error {
	return p.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		if err := releaseAttempt(ctx, tx, attempted, p.Clock.Now()); err != nil {
			return err
		}
		entry, err := tx.GetKV(ctx, ReportsNamespace, key)
		if core.HasCode(err, core.CodeNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var stored storedReport
		if json.Unmarshal(entry.Value, &stored) != nil || !stored.Publish.AttemptedAt.Equal(attempted) {
			return nil
		}
		stored.Publish.AttemptedAt = time.Time{}
		stored.Publish.Unanswered, stored.Publish.UnansweredAt = 0, time.Time{}
		return putStored(ctx, tx, key, entry.Version, stored)
	})
}

// reject counts GitHub's refusal of the report stored at key, with code,
// and takes back the charge of the attempt it refused. Once it was refused
// maxReportRejections times, or at once when it cannot be read, it is
// moved under f/, still counted and pruned with the others, and not tried
// again.
func (p ReportPublisher) reject(ctx context.Context, key string, code core.ErrorCode) error {
	return p.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, ReportsNamespace, key)
		if core.HasCode(err, core.CodeNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var stored storedReport
		readable := json.Unmarshal(entry.Value, &stored) == nil
		unreadable := !readable || code == core.CodeTampered
		if err := releaseAttempt(ctx, tx, stored.Publish.AttemptedAt, p.Clock.Now()); err != nil {
			return err
		}
		stored.Publish.AttemptedAt = time.Time{}
		stored.Publish.Rejections++
		if !unreadable && stored.Publish.Rejections < maxReportRejections {
			return putStored(ctx, tx, key, entry.Version, stored)
		}
		if err := tx.DeleteKV(ctx, ReportsNamespace, key, entry.Version); err != nil {
			return err
		}
		if err := addFailed(ctx, tx, 1); err != nil {
			return err
		}
		// A report given up no longer waits, so its client is forgotten.
		// What an unreadable report says of its client and fingerprint
		// cannot be trusted, so it is forgotten by its key wherever it is
		// listed, and its fingerprint no longer names it: the same report
		// sent again is a new one.
		if unreadable {
			if err := forgetWaitingAnywhere(ctx, tx, key); err != nil {
				return err
			}
			if err := dropIndexOf(ctx, tx, key); err != nil {
				return err
			}
		} else if err := forgetWaiting(ctx, tx, stored.Client, key); err != nil {
			return err
		}
		stored.Client = ""
		failedKey := reportFailedPrefix + strings.TrimPrefix(key, reportPrefix)
		stored.Publish.FailedCode = code
		if !readable {
			// Nothing of it can be shown; keep the bytes under the key.
			_, err := tx.PutKV(ctx, state.KVEntry{Namespace: ReportsNamespace, Key: failedKey, Value: entry.Value})
			return err
		}
		if err := putStored(ctx, tx, failedKey, 0, stored); err != nil || unreadable {
			return err
		}
		return putIndex(ctx, tx, stored.Fingerprint, failedKey)
	})
}

// putIndex points the fingerprint index at key.
func putIndex(ctx context.Context, tx state.Tx, fingerprint, key string) error {
	index := state.KVEntry{Namespace: ReportsNamespace, Key: reportIndexPrefix + fingerprint, Value: []byte(key)}
	existing, err := tx.GetKV(ctx, ReportsNamespace, index.Key)
	switch {
	case err == nil:
		index.Version = existing.Version
	case !core.HasCode(err, core.CodeNotFound):
		return err
	}
	_, err = tx.PutKV(ctx, index)
	return err
}

// answeredWithClientError reports whether err carries GitHub's answer
// with a client error (4xx), a rate limit the client gave up waiting out
// included: GitHub answered, and opened no issue.
func answeredWithClientError(err error) bool {
	if core.HasCode(err, core.CodeRateLimited) {
		return true
	}
	status, ok := githubStatus(err)
	return ok && status >= 400 && status < 500
}

// githubStatus is the HTTP status of GitHub's answer err carries.
func githubStatus(err error) (int, bool) {
	var typed *core.Error
	if !errors.As(err, &typed) {
		return 0, false
	}
	status, convErr := strconv.Atoi(typed.Details["status"])
	return status, convErr == nil
}

// refusesReport reports whether err is GitHub refusing to open the issue
// of one report: a client error (4xx) other than those that hold for every
// report, a refused token (401), a missing permission or a rate limit
// (403, 429) and a repository not found (404, 410). An outage (5xx) or an
// error without GitHub's answer is not one either.
func refusesReport(err error) bool {
	status, ok := githubStatus(err)
	if !ok || status < 400 || status >= 500 {
		return false
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone, http.StatusTooManyRequests:
		return false
	}
	return true
}
