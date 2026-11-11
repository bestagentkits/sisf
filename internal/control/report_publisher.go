package control

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
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
	// full store drains over many passes and never bursts GitHub.
	DefaultReportPublishBatch = 5
	// maxReportRejections is how many times GitHub may refuse a report's
	// issue as invalid before the report is given up: one refusal may be
	// GitHub's passing state, a repeated one is about the report.
	maxReportRejections = 3
	// reportLookupSlack is how long before an unanswered attempt the
	// lookup of the issue it may have opened starts, for clock skew.
	reportLookupSlack = time.Minute
)

// reportIDForm is the form the report endpoint gives report IDs.
var reportIDForm = regexp.MustCompile(`^report-[0-9a-f]{16}$`)

// ReportIssues is what publishing reports does on GitHub: open an issue,
// and list the report issues changed since a time, to find one an attempt
// whose answer was lost may have opened. github.RESTClient is one.
type ReportIssues interface {
	CreateIssue(ctx context.Context, repo github.RepoRef, n github.NewIssue) (github.Issue, error)
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
	// GitHub refused as invalid this time, and Unlabelled the issues opened
	// that GitHub shows without report.IssueLabel, as it does for a token
	// that may open issues but not label them.
	Published  int
	Rejected   int
	Unlabelled int
}

// Publish publishes the oldest reports waiting, at most Batch of them. A
// report whose last attempt got no answer is first looked for among the
// report issues the factory opened since, so an issue opened just before a
// crash is recorded instead of opened again; when GitHub lists more than a
// page of report issues changed since, one such report may be published
// twice. GitHub refusing a report as invalid is counted against it, and
// after maxReportRejections it is kept apart and not tried again. Any other
// failure, an outage or a refused token, ends the call with that error and
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
		var err error
		waiting, err = tx.ListKV(ctx, ReportsNamespace, reportPrefix, batch)
		return err
	})
	if err != nil {
		return result, err
	}
	err = p.publishBatch(ctx, waiting, &result)
	if noted := p.noteError(ctx, err); err == nil {
		err = noted
	}
	return result, err
}

// publishBatch publishes the reports stored at waiting, oldest first, and
// stops at the first failure that is not about one report.
func (p ReportPublisher) publishBatch(ctx context.Context, waiting []state.KVEntry, result *ReportPublishResult) error {
	for _, entry := range waiting {
		published, labelled, err := p.publishOne(ctx, entry)
		switch {
		case core.HasCode(err, core.CodeConflict):
			// The report changed meanwhile, a duplicate counted; the next
			// call reads it again.
		case isRejection(err):
			result.Rejected++
			if err := p.reject(ctx, entry.Key, core.CodeOf(err)); err != nil && !core.HasCode(err, core.CodeConflict) {
				return err
			}
		case err != nil:
			return err
		case published:
			result.Published++
			if !labelled {
				result.Unlabelled++
			}
		}
	}
	return nil
}

// reportPublishErrorKey holds why the last publication failed, for the
// factory's status; absent while it succeeds.
const reportPublishErrorKey = "publish-error"

// noteError records why publishing failed, or that it no longer does,
// writing only when that changed.
func (p ReportPublisher) noteError(ctx context.Context, failure error) error {
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

// ReportPublication is how the publication of stored reports stands, as
// the factory's status shows it.
type ReportPublication struct {
	// Waiting counts the stored reports not published yet.
	Waiting int
	// LastError is why the last publication failed; "" when it did not.
	LastError string
}

// ReadReportPublication reads how the publication of the reports stored in
// store stands.
func ReadReportPublication(ctx context.Context, store state.Store) (ReportPublication, error) {
	var out ReportPublication
	err := store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		waiting, err := tx.ListKV(ctx, ReportsNamespace, reportPrefix, 0)
		if err != nil {
			return err
		}
		out.Waiting = len(waiting)
		entry, err := tx.GetKV(ctx, ReportsNamespace, reportPublishErrorKey)
		switch {
		case err == nil:
			out.LastError = string(entry.Value)
		case !core.HasCode(err, core.CodeNotFound):
			return err
		}
		return nil
	})
	return out, err
}

// publishOne publishes the report stored at entry and reports whether it
// is now shown as an issue, and whether that issue carries the label.
func (p ReportPublisher) publishOne(ctx context.Context, entry state.KVEntry) (published, labelled bool, err error) {
	var stored storedReport
	if err := json.Unmarshal(entry.Value, &stored); err != nil || !reportIDForm.MatchString(stored.ID) || stored.Fingerprint == "" {
		return false, false, p.reject(ctx, entry.Key, core.CodeTampered)
	}
	if !stored.Publish.AttemptedAt.IsZero() {
		issue, found, err := p.findIssue(ctx, stored)
		if err != nil {
			return false, false, err
		}
		if found {
			return true, issue.HasLabel(report.IssueLabel), p.markPublished(ctx, entry.Key, stored, issue.Ref.Number)
		}
	}
	attempt := stored
	attempt.Publish.AttemptedAt = p.Clock.Now().UTC()
	if err := p.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		return putStored(ctx, tx, entry.Key, entry.Version, attempt)
	}); err != nil {
		return false, false, err
	}
	shown := stored.Report
	shown.Title, shown.Body = p.Redactor.Redact(shown.Title), p.Redactor.Redact(shown.Body)
	issue, err := p.Issues.CreateIssue(ctx, p.Repo, github.NewIssue{
		Title: reportIssueTitle(shown), Body: reportIssueBody(shown), Labels: []string{report.IssueLabel}})
	if err != nil {
		return false, false, err
	}
	return true, issue.HasLabel(report.IssueLabel), p.markPublished(ctx, entry.Key, stored, issue.Ref.Number)
}

// findIssue looks for the issue an unanswered attempt to publish r may have
// opened: a report issue the factory's account wrote whose body opens with
// r's marker. Only the publisher writes that first line, so a report's
// text, which may name any ID, cannot pass for it.
func (p ReportPublisher) findIssue(ctx context.Context, r storedReport) (github.Issue, bool, error) {
	page, err := p.Issues.PollIssues(ctx, p.Repo, github.ListIssuesOptions{
		Labels: []string{report.IssueLabel}, Since: r.Publish.AttemptedAt.Add(-reportLookupSlack), MaxPages: 1}, "")
	if err != nil {
		return github.Issue{}, false, err
	}
	marker := report.IssueMarker(r.ID) + "\n"
	for _, issue := range page.Issues {
		if p.Self.Wrote(issue.Author) && strings.HasPrefix(issue.Body, marker) {
			return issue, true, nil
		}
	}
	return github.Issue{}, false, nil
}

// markPublished replaces the report stored at key with what answers a
// duplicate of it, the issue it is shown as included, in one write. The
// report may have been counted again, pruned or pushed out meanwhile; what
// answers a duplicate is recorded all the same.
func (p ReportPublisher) markPublished(ctx context.Context, key string, r storedReport, number int) error {
	return p.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		count, err := readCount(ctx, tx)
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
		stored.Publish.AttemptedAt = time.Time{}
		stored.Publish.Rejections++
		if readable && code != core.CodeTampered && stored.Publish.Rejections < maxReportRejections {
			return putStored(ctx, tx, key, entry.Version, stored)
		}
		if err := tx.DeleteKV(ctx, ReportsNamespace, key, entry.Version); err != nil {
			return err
		}
		failedKey := reportFailedPrefix + strings.TrimPrefix(key, reportPrefix)
		stored.Publish.FailedCode = code
		if !readable {
			// Nothing of it can be shown; keep the bytes under the key.
			_, err := tx.PutKV(ctx, state.KVEntry{Namespace: ReportsNamespace, Key: failedKey, Value: entry.Value})
			return err
		}
		if err := putStored(ctx, tx, failedKey, 0, stored); err != nil {
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

// isRejection reports whether GitHub refused the issue as invalid (422), a
// refusal about the report itself rather than GitHub's or the token's state.
func isRejection(err error) bool {
	var typed *core.Error
	if !core.HasCode(err, core.CodeInvalidArgument) || !errors.As(err, &typed) {
		return false
	}
	return typed.Details["status"] == "422"
}
