package control

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// ReportsNamespace is the namespace of the control zone's store that holds
// the public bug reports. Only the control zone reads or writes it.
const ReportsNamespace = "control.reports"

// DefaultMaxStoredReports bounds the reports kept when ReportRoute leaves
// MaxStored at zero.
const DefaultMaxStoredReports = 1000

// Keys of the reports namespace. A report lives under the UTC day it was
// first received, so the oldest sort first and lapsed ones are pruned from
// the front: under r/ while it waits to be published as a GitHub issue,
// under f/ once GitHub refused it for good, and, once published, only as a
// small record under p/ that answers a duplicate. An index by fingerprint
// finds any of them again for a duplicate, and one counter holds how many
// reports are stored under r/ and f/, so the capacity check reads one
// document; a published report no longer counts.
const (
	reportDayLayout     = "20060102"
	reportPrefix        = "r/"
	reportFailedPrefix  = "f/"
	reportPublishPrefix = "p/"
	reportIndexPrefix   = "fp/"
	reportCountKey      = "count"
	// reportPruneBatch bounds how many lapsed reports one save, or one
	// round of the periodic prune, removes, and so how many documents it
	// reads to find them.
	reportPruneBatch = 16
	// reportPruneRounds bounds the rounds of one periodic prune, so a pass
	// removes at most reportPruneRounds*reportPruneBatch lapsed reports and
	// the next pass goes on.
	reportPruneRounds = 8
	// reportSaveAttempts bounds the retries of a save that lost a race with
	// another one.
	reportSaveAttempts = 3
)

// storedReport is a report as the namespace keeps it under r/ or f/: the
// report and how its publication went so far.
type storedReport struct {
	report.Report
	Publish reportPublishState `json:"publish,omitzero"`
}

// reportPublishState is how the publication of a stored report went so far.
type reportPublishState struct {
	// AttemptedAt is when an issue was last asked for and no answer that
	// says whether GitHub created it was recorded; zero otherwise.
	AttemptedAt time.Time `json:"attempted_at,omitzero"`
	// Rejections counts the times GitHub refused the issue as invalid.
	Rejections int `json:"rejections,omitempty"`
	// FailedCode is the code GitHub's last refusal carried, set once the
	// report was given up and moved under f/.
	FailedCode core.ErrorCode `json:"failed_code,omitempty"`
}

// publishedReport is what is kept of a report once it was published: what
// answers a duplicate, without its text.
type publishedReport struct {
	ID          string    `json:"id"`
	Fingerprint string    `json:"fingerprint"`
	Issue       int       `json:"issue"`
	ReceivedAt  time.Time `json:"received_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	Count       int       `json:"count"`
}

// reportKey is the key of a report first received at under prefix.
func reportKey(prefix string, at time.Time, fingerprint string) string {
	return prefix + at.UTC().Format(reportDayLayout) + "/" + fingerprint
}

// storedReports is a report.Store over the control zone's operational
// store. It is bounded by capacity and by retention, counted from when a
// report was first received.
type storedReports struct {
	store     state.Store
	capacity  int
	retention time.Duration
}

var _ report.Store = storedReports{}

// Save implements report.Store. It first removes lapsed reports; then a
// duplicate is counted against the stored report, published or not, and a
// new one is stored to be published. When the store is full, the oldest
// report GitHub refused for good makes room for it; a report accepted and
// still waiting to be published is never removed, so with none refused the
// new report is refused with LIMIT_EXCEEDED until reports are published or
// lapse.
func (s storedReports) Save(ctx context.Context, r report.Report) (report.Report, bool, error) {
	if r.Fingerprint == "" || strings.ContainsAny(r.Fingerprint, "/") {
		return report.Report{}, false, core.NewError(core.CodeInvalidArgument, "a report needs a fingerprint")
	}
	var (
		stored  report.Report
		created bool
		full    bool
		err     error
	)
	for range reportSaveAttempts {
		err = s.store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			stored, created, full = report.Report{}, false, false
			count, pruned, err := s.pruneLapsed(ctx, tx, r.ReceivedAt)
			if err != nil {
				return err
			}
			found, ok, err := s.countDuplicate(ctx, tx, r)
			if err != nil {
				return err
			}
			if ok {
				stored = found
				return s.putCountIf(ctx, tx, count, pruned)
			}
			room := count.n < s.capacity
			if !room {
				if room, err = s.dropOldest(ctx, tx, &count); err != nil {
					return err
				}
			}
			if !room {
				// Only the new report is refused; the pruning commits.
				full = true
				return s.putCountIf(ctx, tx, count, pruned)
			}
			r.Count, r.LastSeenAt = 1, r.ReceivedAt
			key := reportKey(reportPrefix, r.ReceivedAt, r.Fingerprint)
			if err := putStored(ctx, tx, key, 0, storedReport{Report: r}); err != nil {
				return err
			}
			index := state.KVEntry{Namespace: ReportsNamespace, Key: reportIndexPrefix + r.Fingerprint, Value: []byte(key)}
			if _, err := tx.PutKV(ctx, index); err != nil {
				return err
			}
			count.n++
			stored, created = r, true
			return putCount(ctx, tx, count)
		})
		if !core.HasCode(err, core.CodeConflict) {
			break
		}
	}
	switch {
	case err != nil:
		return report.Report{}, false, err
	case full:
		return report.Report{}, false, core.NewError(core.CodeLimitExceeded, "the report store is full")
	}
	return stored, created, nil
}

// dropOldest removes, to make room, the oldest report GitHub refused for
// good, and reports whether it found one. A report accepted and still
// waiting to be published is never removed to make room, and a published
// one is not counted.
func (s storedReports) dropOldest(ctx context.Context, tx state.Tx, count *reportCount) (bool, error) {
	entries, err := tx.ListKV(ctx, ReportsNamespace, reportFailedPrefix, 1)
	if err != nil || len(entries) == 0 {
		return false, err
	}
	if err := dropReport(ctx, tx, entries[0]); err != nil {
		return false, err
	}
	count.n = max(count.n-1, 0)
	return true, nil
}

// putCountIf stores count when changed says it changed.
func (s storedReports) putCountIf(ctx context.Context, tx state.Tx, count reportCount, changed bool) error {
	if !changed {
		return nil
	}
	return putCount(ctx, tx, count)
}

// dropReport removes the stored entry e and its fingerprint index, when
// the index still names e.
func dropReport(ctx context.Context, tx state.Tx, e state.KVEntry) error {
	if err := tx.DeleteKV(ctx, ReportsNamespace, e.Key, e.Version); err != nil {
		return err
	}
	fingerprint := e.Key[strings.LastIndex(e.Key, "/")+1:]
	index, err := tx.GetKV(ctx, ReportsNamespace, reportIndexPrefix+fingerprint)
	switch {
	case err == nil && string(index.Value) == e.Key:
		return tx.DeleteKV(ctx, ReportsNamespace, index.Key, index.Version)
	case err == nil || core.HasCode(err, core.CodeNotFound):
		return nil
	}
	return err
}

// countDuplicate counts r against the stored report with its fingerprint,
// when there is one, keeping how its publication went; a published one is
// counted in what is kept of it.
func (s storedReports) countDuplicate(ctx context.Context, tx state.Tx, r report.Report) (report.Report, bool, error) {
	index, err := tx.GetKV(ctx, ReportsNamespace, reportIndexPrefix+r.Fingerprint)
	if core.HasCode(err, core.CodeNotFound) {
		return report.Report{}, false, nil
	}
	if err != nil {
		return report.Report{}, false, err
	}
	entry, err := tx.GetKV(ctx, ReportsNamespace, string(index.Value))
	if err != nil {
		return report.Report{}, false, err
	}
	if strings.HasPrefix(entry.Key, reportPublishPrefix) {
		var kept publishedReport
		if err := json.Unmarshal(entry.Value, &kept); err != nil {
			return report.Report{}, false, core.NewError(core.CodeTampered, "a published report's record cannot be read").WithCause(err)
		}
		kept.Count++
		kept.LastSeenAt = r.ReceivedAt
		if err := putJSON(ctx, tx, entry.Key, entry.Version, kept); err != nil {
			return report.Report{}, false, err
		}
		return report.Report{ID: kept.ID, Fingerprint: kept.Fingerprint, ReceivedAt: kept.ReceivedAt,
			LastSeenAt: kept.LastSeenAt, Count: kept.Count}, true, nil
	}
	var stored storedReport
	if err := json.Unmarshal(entry.Value, &stored); err != nil {
		return report.Report{}, false, core.NewError(core.CodeTampered, "a stored report cannot be read").WithCause(err)
	}
	stored.Count++
	stored.LastSeenAt = r.ReceivedAt
	return stored.Report, true, putStored(ctx, tx, entry.Key, entry.Version, stored)
}

// pruneLapsed removes reports first received on a day before the retention
// began, the oldest first and at most reportPruneBatch of each kind: those
// waiting to be published, those GitHub refused and what is kept of
// published ones. It returns the count of stored reports left and whether
// it removed any.
func (s storedReports) pruneLapsed(ctx context.Context, tx state.Tx, now time.Time) (reportCount, bool, error) {
	count, err := readCount(ctx, tx)
	if err != nil {
		return count, false, err
	}
	day := now.Add(-s.retention).UTC().Format(reportDayLayout) + "/"
	pruned := false
	for _, prefix := range []string{reportPrefix, reportFailedPrefix, reportPublishPrefix} {
		entries, err := tx.ListKV(ctx, ReportsNamespace, prefix, reportPruneBatch)
		if err != nil {
			return count, pruned, err
		}
		for _, e := range entries {
			if e.Key >= prefix+day {
				break
			}
			if err := dropReport(ctx, tx, e); err != nil {
				return count, pruned, err
			}
			if prefix != reportPublishPrefix {
				count.n = max(count.n-1, 0)
			}
			pruned = true
		}
	}
	return count, pruned, nil
}

// pruneLapsedReports removes the reports past their retention at now, in
// rounds of at most reportPruneBatch and at most reportPruneRounds of them.
// It is what the control zone's periodic pass calls, so a report does not
// outlive its retention when no new report arrives to prune it.
func (s storedReports) pruneLapsedReports(ctx context.Context, now time.Time) error {
	for range reportPruneRounds {
		var pruned bool
		err := s.store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			count, removed, err := s.pruneLapsed(ctx, tx, now)
			if err != nil || !removed {
				return err
			}
			pruned = true
			return putCount(ctx, tx, count)
		})
		if err != nil || !pruned {
			return err
		}
	}
	return nil
}

// reportCount is the stored number of reports and the version it was read at.
type reportCount struct {
	n       int
	version int64
}

func readCount(ctx context.Context, tx state.Tx) (reportCount, error) {
	entry, err := tx.GetKV(ctx, ReportsNamespace, reportCountKey)
	if core.HasCode(err, core.CodeNotFound) {
		return reportCount{}, nil
	}
	if err != nil {
		return reportCount{}, err
	}
	n, err := strconv.Atoi(string(entry.Value))
	if err != nil || n < 0 {
		return reportCount{}, core.NewError(core.CodeTampered, "the stored report count cannot be read")
	}
	return reportCount{n: n, version: entry.Version}, nil
}

func putCount(ctx context.Context, tx state.Tx, c reportCount) error {
	_, err := tx.PutKV(ctx, state.KVEntry{Namespace: ReportsNamespace, Key: reportCountKey,
		Value: []byte(strconv.Itoa(c.n)), Version: c.version})
	return err
}

// putStored writes a report waiting to be published, or refused, at key.
func putStored(ctx context.Context, tx state.Tx, key string, version int64, r storedReport) error {
	return putJSON(ctx, tx, key, version, r)
}

// putJSON writes v at key of the reports namespace.
func putJSON(ctx context.Context, tx state.Tx, key string, version int64, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return core.NewError(core.CodeInternal, "a report cannot be encoded").WithCause(err)
	}
	_, err = tx.PutKV(ctx, state.KVEntry{Namespace: ReportsNamespace, Key: key, Value: raw, Version: version})
	return err
}
