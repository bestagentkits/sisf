package control

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// ReportDailyPublishBudget bounds the report issues the publisher opens in
// any 24 hours, on top of the spacing between publications. Anyone may send
// a report, so whatever the route accepts, the factory's token spends at
// most this much of its content-creating budget on reports a day. Once it
// is spent, publishing pauses until the oldest publication counted is a
// day old; nothing waiting is dropped.
const ReportDailyPublishBudget = 50

const (
	// reportBudgetPeriod is the rolling period of the publication budget.
	reportBudgetPeriod = 24 * time.Hour
	// reportBudgetKey holds when the report issues of the last period were
	// opened, so a restart does not reset the budget.
	reportBudgetKey = "publish-budget"
)

// publishBudget is when the report issues of the period up to a moment
// were opened, oldest first.
type publishBudget struct {
	entry state.KVEntry
	times []time.Time
}

// readPublishBudget reads the publications counted against the budget at
// now: those opened less than reportBudgetPeriod before it.
func readPublishBudget(ctx context.Context, tx state.Tx, now time.Time) (publishBudget, error) {
	entry, err := tx.GetKV(ctx, ReportsNamespace, reportBudgetKey)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return publishBudget{entry: state.KVEntry{Namespace: ReportsNamespace, Key: reportBudgetKey}}, nil
	case err != nil:
		return publishBudget{}, err
	}
	var times []time.Time
	if err := json.Unmarshal(entry.Value, &times); err != nil {
		return publishBudget{}, core.NewError(core.CodeTampered, "the stored budget of report publications cannot be read").WithCause(err)
	}
	slices.SortFunc(times, func(a, b time.Time) int { return a.Compare(b) })
	recent := slices.DeleteFunc(times, func(t time.Time) bool { return !now.Before(t.Add(reportBudgetPeriod)) })
	return publishBudget{entry: entry, times: recent}, nil
}

// remaining is how many report issues may still be opened.
func (b publishBudget) remaining() int {
	return max(ReportDailyPublishBudget-len(b.times), 0)
}

// pausedUntil is when the next report issue may be opened while the budget
// is spent; zero while it is not.
func (b publishBudget) pausedUntil() time.Time {
	if b.remaining() > 0 {
		return time.Time{}
	}
	return b.times[len(b.times)-ReportDailyPublishBudget].Add(reportBudgetPeriod)
}

// chargePublication counts a report issue opened at now against the
// budget, in the transaction that records the report as published.
func chargePublication(ctx context.Context, tx state.Tx, now time.Time) error {
	budget, err := readPublishBudget(ctx, tx, now)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(append(budget.times, now.UTC()))
	if err != nil {
		return core.NewError(core.CodeInternal, "the budget of report publications cannot be encoded").WithCause(err)
	}
	budget.entry.Value = raw
	_, err = tx.PutKV(ctx, budget.entry)
	return err
}

// ReportPublication is how the publication of stored reports stands, as
// the factory's status shows it.
type ReportPublication struct {
	// Waiting counts the stored reports not published yet, Failed those
	// given up after GitHub refused them, which will never be published.
	Waiting int
	Failed  int
	// PausedUntil is when publishing goes on while the daily budget of
	// report issues is spent; zero while it is not.
	PausedUntil time.Time
	// LastError is why the last publication failed; "" when it did not.
	LastError string
}

// ReadReportPublication reads how the publication of the reports stored in
// store stands at now. It reads counters and the budget, never a report.
func ReadReportPublication(ctx context.Context, store state.Store, now time.Time) (ReportPublication, error) {
	var out ReportPublication
	err := store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		stored, err := readCounter(ctx, tx, reportCountKey)
		if err != nil {
			return err
		}
		failed, err := readCounter(ctx, tx, reportFailedKey)
		if err != nil {
			return err
		}
		out.Waiting, out.Failed = max(stored.n-failed.n, 0), failed.n
		budget, err := readPublishBudget(ctx, tx, now)
		if err != nil {
			return err
		}
		out.PausedUntil = budget.pausedUntil()
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
