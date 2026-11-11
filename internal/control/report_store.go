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
// the front; an index by fingerprint finds it again for a duplicate, and one
// counter holds how many are stored, so the capacity check reads one
// document.
const (
	reportDayLayout   = "20060102"
	reportPrefix      = "r/"
	reportIndexPrefix = "fp/"
	reportCountKey    = "count"
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
// duplicate is counted against the stored report, and a new one is stored.
// When the store is full, a report of the oldest day stored makes room for
// it: reports are kept by the UTC day they arrived, so which of that day's
// goes is not ordered further. The report route's per-client and overall
// rate limits bound how fast reports can push others out. Only a store
// whose count says full while it lists no report refuses a new one, with
// LIMIT_EXCEEDED.
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
			key := reportPrefix + r.ReceivedAt.UTC().Format(reportDayLayout) + "/" + r.Fingerprint
			if err := putReport(ctx, tx, key, 0, r); err != nil {
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

// dropOldest removes one report of the oldest day stored to make room and
// reports whether it found one.
func (s storedReports) dropOldest(ctx context.Context, tx state.Tx, count *reportCount) (bool, error) {
	entries, err := tx.ListKV(ctx, ReportsNamespace, reportPrefix, 1)
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

// dropReport removes the stored report e and its fingerprint index.
func dropReport(ctx context.Context, tx state.Tx, e state.KVEntry) error {
	if err := tx.DeleteKV(ctx, ReportsNamespace, e.Key, e.Version); err != nil {
		return err
	}
	fingerprint := e.Key[strings.LastIndex(e.Key, "/")+1:]
	index, err := tx.GetKV(ctx, ReportsNamespace, reportIndexPrefix+fingerprint)
	switch {
	case err == nil:
		return tx.DeleteKV(ctx, ReportsNamespace, index.Key, index.Version)
	case core.HasCode(err, core.CodeNotFound):
		return nil
	}
	return err
}

// countDuplicate counts r against the stored report with its fingerprint,
// when there is one.
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
	var stored report.Report
	if err := json.Unmarshal(entry.Value, &stored); err != nil {
		return report.Report{}, false, core.NewError(core.CodeTampered, "a stored report cannot be read").WithCause(err)
	}
	stored.Count++
	stored.LastSeenAt = r.ReceivedAt
	return stored, true, putReport(ctx, tx, entry.Key, entry.Version, stored)
}

// pruneLapsed removes reports first received on a day before the retention
// began, the oldest first and at most reportPruneBatch of them. It returns
// the count of those left and whether it removed any.
func (s storedReports) pruneLapsed(ctx context.Context, tx state.Tx, now time.Time) (reportCount, bool, error) {
	count, err := readCount(ctx, tx)
	if err != nil {
		return count, false, err
	}
	oldest := reportPrefix + now.Add(-s.retention).UTC().Format(reportDayLayout) + "/"
	entries, err := tx.ListKV(ctx, ReportsNamespace, reportPrefix, reportPruneBatch)
	if err != nil {
		return count, false, err
	}
	pruned := false
	for _, e := range entries {
		if e.Key >= oldest {
			break
		}
		if err := dropReport(ctx, tx, e); err != nil {
			return count, pruned, err
		}
		count.n, pruned = max(count.n-1, 0), true
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

func putReport(ctx context.Context, tx state.Tx, key string, version int64, r report.Report) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return core.NewError(core.CodeInternal, "a report cannot be encoded").WithCause(err)
	}
	_, err = tx.PutKV(ctx, state.KVEntry{Namespace: ReportsNamespace, Key: key, Value: raw, Version: version})
	return err
}
