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
// any 24 hours, on top of the spacing between publications. Each attempt
// to open one is charged when it is recorded, in the same write, and the
// charge is taken back when GitHub refuses the issue. Anyone may send
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
// now: those opened less than reportBudgetPeriod before it. One dated
// ahead of now was charged before the clock was stepped back. It still
// counts, so a step back never opens room for a second budget; it lapses
// a period after its own date, so publishing may stay paused for as much
// longer as the clock was stepped back.
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

// reserveAttempt checks and charges the budget for an issue asked for at
// now, in the transaction that records the attempt, so publishers sharing
// the store cannot together exceed it. Every attempt is charged, one whose
// answer was lost included: whether it opened an issue is not known, and
// another publisher may still be waiting for its answer. previous, when
// not zero, is the report's earlier attempt, which a lookup showed opened
// no issue: its charge is taken back in the same write, so a report tried
// again holds one charge, not one for every try. It reports whether the
// budget had room; when it had none, nothing is written.
func reserveAttempt(ctx context.Context, tx state.Tx, previous, now time.Time) (bool, error) {
	budget, err := readPublishBudget(ctx, tx, now)
	if err != nil {
		return false, err
	}
	times, _ := withoutAttempt(budget.times, previous)
	if len(times) >= ReportDailyPublishBudget {
		return false, nil
	}
	return true, budget.put(ctx, tx, append(times, now.UTC()))
}

// releaseAttempt takes back the charge of an attempt made at attempted
// that opened no issue, as GitHub's refusal or a lookup shows. A zero time
// names none.
func releaseAttempt(ctx context.Context, tx state.Tx, attempted, now time.Time) error {
	if attempted.IsZero() {
		return nil
	}
	budget, err := readPublishBudget(ctx, tx, now)
	if err != nil {
		return err
	}
	times, charged := withoutAttempt(budget.times, attempted)
	if !charged {
		return nil
	}
	return budget.put(ctx, tx, times)
}

// withoutAttempt returns times without the charge of the attempt made at
// attempted, and whether it held one. A zero time names none.
func withoutAttempt(times []time.Time, attempted time.Time) ([]time.Time, bool) {
	if attempted.IsZero() {
		return times, false
	}
	i := slices.IndexFunc(times, attempted.Equal)
	if i < 0 {
		return times, false
	}
	return slices.Delete(times, i, i+1), true
}

// put stores times as the budget.
func (b publishBudget) put(ctx context.Context, tx state.Tx, times []time.Time) error {
	raw, err := json.Marshal(times)
	if err != nil {
		return core.NewError(core.CodeInternal, "the budget of report publications cannot be encoded").WithCause(err)
	}
	b.entry.Value = raw
	_, err = tx.PutKV(ctx, b.entry)
	return err
}

// ReportPublication is how the publication of stored reports stands, as
// the factory's status shows it.
type ReportPublication struct {
	// Waiting counts the stored reports not published yet, Failed those
	// given up after GitHub refused them, which will never be published.
	Waiting int
	Failed  int
	// Dropped counts the waiting reports pushed out of a full store to
	// make room for another client's report.
	Dropped int
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
		dropped, err := readCounter(ctx, tx, reportDroppedKey)
		if err != nil {
			return err
		}
		out.Waiting, out.Failed, out.Dropped = max(stored.n-failed.n, 0), failed.n, dropped.n
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
