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
}

// Publish publishes the reports waiting in the order they were received,
// at most Batch of them and no more than the daily budget has left
// (ReportDailyPublishBudget); with the budget spent it opens nothing and
// says until when publishing pauses, keeping every report. A report whose
// last attempt got no answer is first looked for among the issues the
// factory's account opened since, by the marker its body opens with and
// with or without the label, so an issue opened just before a crash is
// recorded instead of opened again; only when GitHub lists more than 1,000
// issues changed since may one such report be published twice. An issue
// GitHub shows without the label is given it; when that fails, the
// factory's status says the token may not label issues. GitHub refusing the issue of a report, a client error that is not
// about the token, the repository or the rate limit, is counted against
// that report and the next one is tried; after maxReportRejections it is
// kept apart and not tried again. Any other failure, an outage or a refused
// token, holds for every report: it ends the call with that error and
// leaves the report waiting, for the caller to try again after a pause.
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
	err := p.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		budget, err := readPublishBudget(ctx, tx, p.Clock.Now())
		if err != nil {
			return err
		}
		if budget.remaining() == 0 {
			result.PausedUntil = budget.pausedUntil()
			return nil
		}
		waiting, err = tx.ListKV(ctx, ReportsNamespace, reportPrefix, min(batch, budget.remaining()))
		return err
	})
	if err != nil || !result.PausedUntil.IsZero() {
		return result, err
	}
	err = p.publishBatch(ctx, waiting, &result)
	failure := err
	if failure == nil && result.Unlabelled > 0 {
		failure = missingLabelPermission(result.labelFailure)
	}
	if noted := p.noteError(ctx, failure, result.Published > result.Unlabelled); err == nil {
		err = noted
	}
	if err == nil && result.Published > 0 {
		err = p.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
			budget, err := readPublishBudget(ctx, tx, p.Clock.Now())
			result.PausedUntil = budget.pausedUntil()
			return err
		})
	}
	return result, err
}

// publishBatch publishes the reports stored at waiting, oldest first, and
// stops at the first failure that is not about one report.
func (p ReportPublisher) publishBatch(ctx context.Context, waiting []state.KVEntry, result *ReportPublishResult) error {
	for _, entry := range waiting {
		published, labelFailure, refused, err := p.publishOne(ctx, entry)
		switch {
		case refused != nil:
			result.Rejected++
			if err := p.reject(ctx, entry.Key, core.CodeOf(refused)); err != nil && !core.HasCode(err, core.CodeConflict) {
				return err
			}
		case core.HasCode(err, core.CodeConflict):
			// The report changed meanwhile, a duplicate counted; the next
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

// publishOne publishes the report stored at entry and reports whether it
// is now shown as an issue, and, when that issue does not carry the label
// and it could not be added, why (labelFailure). GitHub's refusal of the
// report's issue is returned as refused, apart from the failures that hold
// for every report.
func (p ReportPublisher) publishOne(ctx context.Context, entry state.KVEntry) (published bool, labelFailure, refused, err error) {
	var stored storedReport
	if err := json.Unmarshal(entry.Value, &stored); err != nil || !reportIDForm.MatchString(stored.ID) || stored.Fingerprint == "" {
		return false, nil, nil, p.reject(ctx, entry.Key, core.CodeTampered)
	}
	if !stored.Publish.AttemptedAt.IsZero() {
		issue, found, err := p.findIssue(ctx, stored)
		if err != nil {
			return false, nil, nil, err
		}
		if found {
			labelFailure, err := p.recordIssue(ctx, entry.Key, stored, issue)
			return true, labelFailure, nil, err
		}
	}
	attempt := stored
	attempt.Publish.AttemptedAt = p.Clock.Now().UTC()
	if err := p.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		return putStored(ctx, tx, entry.Key, entry.Version, attempt)
	}); err != nil {
		return false, nil, nil, err
	}
	// The text is made visible and redacted again, as the route does, in
	// case it was stored by an older route, and redacted once more after
	// it is defused for GitHub.
	shown := stored.Report
	shown.Title = p.Redactor.Redact(report.Visible(shown.Title))
	shown.Body = p.Redactor.Redact(report.Visible(shown.Body))
	issue, err := p.Issues.CreateIssue(ctx, p.Repo, github.NewIssue{
		Title: reportIssueTitle(shown, p.Redactor.Redact), Body: reportIssueBody(shown, p.Redactor.Redact), Labels: []string{report.IssueLabel}})
	switch {
	case refusesReport(err):
		return false, nil, err, nil
	case err != nil:
		return false, nil, nil, err
	}
	labelFailure, err = p.recordIssue(ctx, entry.Key, stored, issue)
	return true, labelFailure, nil, err
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
// duplicate of it, the issue it is shown as included, and counts the issue
// against the daily budget, in one write. The
// report may have been counted again, pruned or pushed out meanwhile; what
// answers a duplicate is recorded all the same.
func (p ReportPublisher) markPublished(ctx context.Context, key string, r storedReport, number int) error {
	return p.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		if err := chargePublication(ctx, tx, p.Clock.Now()); err != nil {
			return err
		}
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

// reject counts GitHub's refusal of the report stored at key, with code.
// Once it was refused maxReportRejections times, or at once when it cannot
// be read, it is moved under f/, still counted and pruned with the others,
// and not tried again.
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

// refusesReport reports whether err is GitHub refusing to open the issue
// of one report: a client error (4xx) other than those that hold for every
// report, a refused token (401), a missing permission or a rate limit
// (403, 429) and a repository not found (404, 410). An outage (5xx) or an
// error without GitHub's answer is not one either.
func refusesReport(err error) bool {
	var typed *core.Error
	if !errors.As(err, &typed) {
		return false
	}
	status, convErr := strconv.Atoi(typed.Details["status"])
	if convErr != nil || status < 400 || status >= 500 {
		return false
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone, http.StatusTooManyRequests:
		return false
	}
	return true
}
