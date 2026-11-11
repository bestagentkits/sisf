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

// Keys of the reports namespace. A report lives under the UTC day and then
// the instant it was first received, its fingerprint last, so reports sort
// in the order they were received, which nobody sending them can choose,
// and lapsed ones are pruned from the front: under r/ while it waits to be
// published as a GitHub issue, under f/ once GitHub refused it for good,
// and, once published, only as a small record under p/ that answers a
// duplicate. An index by fingerprint finds any of them again for a
// duplicate. One counter holds how many reports are stored under r/ and
// f/, so the capacity check reads one document, and another how many of
// them are under f/, so the factory's status reads no report; a published
// report no longer counts.
const (
	reportDayLayout     = "20060102"
	reportTimeLayout    = "150405.000000000"
	reportPrefix        = "r/"
	reportFailedPrefix  = "f/"
	reportPublishPrefix = "p/"
	reportIndexPrefix   = "fp/"
	reportCountKey      = "count"
	reportFailedKey     = "failed-count"
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

// reportKey is the key of a report first received at under prefix: the
// day, the instant within it and the fingerprint, so keys sort in the order
// reports were received and the day stays a prefix for pruning.
func reportKey(prefix string, at time.Time, fingerprint string) string {
	at = at.UTC()
	return prefix + at.Format(reportDayLayout) + "/" + at.Format(reportTimeLayout) + "/" + fingerprint
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
// new one is stored to be published, its client kept only as a keyed hash
// (clientHash). A client that already has maxWaitingPerClient reports
// waiting is refused with RATE_LIMITED and a time to retry, read from its
// list of waiting reports alone. When the store is full, the oldest report GitHub refused
// for good makes room for it, and else the newest waiting report of the
// client holding the most, as long as that client holds more than one and
// more than the new report's client (dropFromBusiestClient). With no such
// client, as when every waiting report came from another address, the new
// report is refused with LIMIT_EXCEEDED until reports are published or
// lapse. A report whose publication is under way is never removed.
func (s storedReports) Save(ctx context.Context, r report.Report) (report.Report, bool, error) {
	if r.Fingerprint == "" || strings.ContainsAny(r.Fingerprint, "/") {
		return report.Report{}, false, core.NewError(core.CodeInvalidArgument, "a report needs a fingerprint")
	}
	var (
		stored  report.Report
		created bool
		full    bool
		backlog bool
		err     error
	)
	for range reportSaveAttempts {
		err = s.store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			stored, created, full, backlog = report.Report{}, false, false, false
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
			client, err := clientHash(ctx, tx, r.Client)
			if err != nil {
				return err
			}
			// A client holding its share of waiting reports is refused
			// before any room is made, so it pushes out nobody's report.
			held, err := waitingCount(ctx, tx, client)
			if err != nil {
				return err
			}
			if held >= maxWaitingPerClient {
				backlog = true
				return s.putCountIf(ctx, tx, count, pruned)
			}
			room := count.n < s.capacity
			if !room {
				if room, err = s.dropOldest(ctx, tx, &count); err != nil {
					return err
				}
			}
			if !room {
				if room, err = s.dropFromBusiestClient(ctx, tx, client, &count); err != nil {
					return err
				}
			}
			if !room {
				// Only the new report is refused; the pruning commits.
				full = true
				return s.putCountIf(ctx, tx, count, pruned)
			}
			r.Count, r.LastSeenAt, r.Client = 1, r.ReceivedAt, client
			key := reportKey(reportPrefix, r.ReceivedAt, r.Fingerprint)
			if err := putStored(ctx, tx, key, 0, storedReport{Report: r}); err != nil {
				return err
			}
			if err := addWaiting(ctx, tx, client, key); err != nil {
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
	case backlog:
		return report.Report{}, false, tooManyWaiting()
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
	return true, addFailed(ctx, tx, -1)
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
	return dropIndexOf(ctx, tx, e.Key)
}

// dropIndexOf removes the fingerprint index of the report stored at key,
// when the index still names key. The fingerprint is read from the key,
// so a report whose value cannot be read loses its index all the same.
func dropIndexOf(ctx context.Context, tx state.Tx, key string) error {
	fingerprint := key[strings.LastIndex(key, "/")+1:]
	index, err := tx.GetKV(ctx, ReportsNamespace, reportIndexPrefix+fingerprint)
	switch {
	case err == nil && string(index.Value) == key:
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
	count, err := readCounter(ctx, tx, reportCountKey)
	if err != nil {
		return count, false, err
	}
	day := now.Add(-s.retention).UTC().Format(reportDayLayout) + "/"
	pruned, failed := false, 0
	for _, prefix := range []string{reportPrefix, reportFailedPrefix, reportPublishPrefix} {
		entries, err := tx.ListKV(ctx, ReportsNamespace, prefix, reportPruneBatch)
		if err != nil {
			return count, pruned, err
		}
		for _, e := range entries {
			if e.Key >= prefix+day {
				break
			}
			if prefix == reportPrefix {
				if err := forgetWaitingEntry(ctx, tx, e); err != nil {
					return count, pruned, err
				}
			}
			if err := dropReport(ctx, tx, e); err != nil {
				return count, pruned, err
			}
			if prefix != reportPublishPrefix {
				count.n = max(count.n-1, 0)
			}
			if prefix == reportFailedPrefix {
				failed++
			}
			pruned = true
		}
	}
	return count, pruned, addFailed(ctx, tx, -failed)
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

// reportCount is a stored number of reports, the key it is stored at and
// the version it was read at.
type reportCount struct {
	key     string
	n       int
	version int64
}

// readCounter reads the counter stored at key; an absent one is zero.
func readCounter(ctx context.Context, tx state.Tx, key string) (reportCount, error) {
	entry, err := tx.GetKV(ctx, ReportsNamespace, key)
	if core.HasCode(err, core.CodeNotFound) {
		return reportCount{key: key}, nil
	}
	if err != nil {
		return reportCount{}, err
	}
	n, err := strconv.Atoi(string(entry.Value))
	if err != nil || n < 0 {
		return reportCount{}, core.NewError(core.CodeTampered, "the stored report count cannot be read")
	}
	return reportCount{key: key, n: n, version: entry.Version}, nil
}

func putCount(ctx context.Context, tx state.Tx, c reportCount) error {
	_, err := tx.PutKV(ctx, state.KVEntry{Namespace: ReportsNamespace, Key: c.key,
		Value: []byte(strconv.Itoa(c.n)), Version: c.version})
	return err
}

// addFailed changes the count of reports given up under f/ by delta.
func addFailed(ctx context.Context, tx state.Tx, delta int) error {
	if delta == 0 {
		return nil
	}
	failed, err := readCounter(ctx, tx, reportFailedKey)
	if err != nil {
		return err
	}
	failed.n = max(failed.n+delta, 0)
	return putCount(ctx, tx, failed)
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
