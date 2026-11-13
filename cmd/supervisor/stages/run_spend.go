package stages

import (
	"context"
	"encoding/json"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// JudgeUsage books what the QA agent reported in the budget ledger, as the
// control zone may, and tallies it per run, so that the run's reservation
// is closed knowing whether its judgments stated a cost. The cost itself is
// read back from the ledger at the close, never from the tally.
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

// closeRun closes the reservation of a run that QA passed or held: either
// way it starts no more coding attempts. The reservation is the one the
// run's claim made, derived from the run's ID; the one the dispatcher's
// record names is not trusted, as the agent zone can rewrite the record.
// What the run spent is read from its rows in the ledger, which hold its
// coding attempts and its judgments alike; the dispatcher's record and the
// judgments' tally only say whether some work stated no cost, which can
// raise the charge and never lower it. A run the dispatcher holds no record
// of is closed from the ledger alone. The run's close-due record, at
// version due, is deleted with it.
func (s *QAStage) closeRun(ctx context.Context, run core.RunID, due int64) error {
	return s.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		if s.Budget == nil {
			return tx.DeleteKV(ctx, Namespace, closeDuePrefix+string(run), due)
		}
		var record dispatcher.RunRecord
		entry, err := tx.GetKV(ctx, handoff.Namespace, "run/"+string(run))
		switch {
		case core.HasCode(err, core.CodeNotFound):
			record.RunID = run
		case err != nil:
			return err
		case json.Unmarshal(entry.Value, &record) != nil || record.RunID != run:
			return core.NewError(core.CodeTampered, "the dispatcher's record of run "+string(run)+" is not a record of that run")
		}
		judged, err := judgeSpend(ctx, tx, run)
		if err != nil {
			return err
		}
		unpriced := record.Spend.Unpriced || judged.Unpriced
		if err := dispatcher.CloseReservationOfRun(ctx, s.Budget, tx, run, budget.RunReservationID(run), unpriced); err != nil {
			return err
		}
		return tx.DeleteKV(ctx, Namespace, closeDuePrefix+string(run), due)
	})
}
