package stages

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// JudgeUsage books what the QA agent reported in the budget ledger, as the
// control zone may, and tallies it per run, so that the run's reservation
// is closed knowing whether its judgments stated a cost.
type JudgeUsage struct{ State state.Store }

// AppendUsage books one usage record of a judgment.
func (j JudgeUsage) AppendUsage(ctx context.Context, u core.Usage) error {
	if u.Step == "" {
		u.Step = core.StepQA
	}
	return j.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		if err := tx.AppendUsage(ctx, u); err != nil || u.RunID == "" {
			return err
		}
		spend, err := judgeSpend(ctx, tx, u.RunID)
		if err != nil {
			return err
		}
		spend.Add([]core.Usage{u}, true)
		return put(ctx, tx, judgeSpendKey(u.RunID), spend)
	})
}

func judgeSpendKey(run core.RunID) string { return "spend/" + string(run) }

// judgeSpend reads the tally of a run's judgments; none yet is zero.
func judgeSpend(ctx context.Context, kv state.KV, run core.RunID) (dispatcher.RunSpend, error) {
	var spend dispatcher.RunSpend
	err := get(ctx, kv, judgeSpendKey(run), &spend)
	if core.HasCode(err, core.CodeNotFound) {
		return dispatcher.RunSpend{}, nil
	}
	return spend, err
}

// closeDuePrefix keys, in Namespace, the runs whose reservation is still to
// be closed: QA passed or held them, and the close has not succeeded yet.
const closeDuePrefix = "close-due/"

// putCloseDue records, with the verdict that ends a run's coding, that its
// reservation is to be closed; the QA stage tries until it succeeds.
func putCloseDue(ctx context.Context, tx state.Tx, run core.RunID) error {
	return put(ctx, tx, closeDuePrefix+string(run), string(run))
}

// closeDueRuns closes the reservation of every run whose close is due, the
// ones a verdict of this pass recorded and those whose close failed before.
// A run is forgotten in the transaction that closes it; one that fails is
// tried again on the next pass.
func (s *QAStage) closeDueRuns(ctx context.Context) []error {
	var due []state.KVEntry
	if err := s.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		due, err = tx.ListKV(ctx, Namespace, closeDuePrefix, 0)
		return err
	}); err != nil {
		return []error{fmt.Errorf("list the runs whose reservation is due to close: %w", err)}
	}
	var failed []error
	for _, entry := range due {
		run := core.RunID(strings.TrimPrefix(entry.Key, closeDuePrefix))
		if err := s.closeRun(ctx, run, entry.Version); err != nil {
			failed = append(failed, fmt.Errorf("%s: close the run's reservation: %w", run, err))
		}
	}
	return failed
}

// closeRun closes the reservation of a run that QA passed or held: either
// way it starts no more coding attempts. The reservation is the one the
// run's claim made, derived from the run's ID; the one the dispatcher's
// record names is not trusted, as the agent zone can rewrite the record. What its coding attempts and its
// judgments reported is counted together. The run's close-due record, at
// version due, is deleted with it.
func (s *QAStage) closeRun(ctx context.Context, run core.RunID, due int64) error {
	return s.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		if s.Budget == nil {
			return tx.DeleteKV(ctx, Namespace, closeDuePrefix+string(run), due)
		}
		entry, err := tx.GetKV(ctx, handoff.Namespace, "run/"+string(run))
		if err != nil {
			return err
		}
		var record dispatcher.RunRecord
		if json.Unmarshal(entry.Value, &record) != nil || record.RunID != run {
			return core.NewError(core.CodeTampered, "the dispatcher's record of run "+string(run)+" is not a record of that run")
		}
		judged, err := judgeSpend(ctx, tx, run)
		if err != nil {
			return err
		}
		spend := dispatcher.RunSpend{
			ReportedCost: record.Spend.ReportedCost + judged.ReportedCost,
			Unpriced:     record.Spend.Unpriced || judged.Unpriced,
		}
		if err := dispatcher.CloseReservationOfRun(ctx, s.Budget, tx, run, budget.RunReservationID(run), spend); err != nil {
			return err
		}
		return tx.DeleteKV(ctx, Namespace, closeDuePrefix+string(run), due)
	})
}
