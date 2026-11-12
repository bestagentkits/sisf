package stages

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

const (
	// closeDuePrefix keys, in Namespace, the runs whose reservation is still
	// to be closed: QA passed or held them, and the close has not succeeded
	// yet.
	closeDuePrefix = "close-due/"
	// closeAbandonedPrefix keys, in Namespace, the runs whose reservation
	// the stage stopped trying to close, with why.
	closeAbandonedPrefix = "close-abandoned/"
	// closeDueBound is how many closes of one run may fail with a code no
	// retry changes before the stage stops trying and records the run as
	// abandoned. A close that fails for a passing reason, such as a busy
	// store, is tried again on every pass and is not counted.
	closeDueBound = 5
)

// closeDue is the record that a run's reservation is still to be closed.
// An entry written before the count was kept holds the run's ID alone, as
// a JSON string, and reads as one with no failure counted.
type closeDue struct {
	RunID core.RunID `json:"run_id"`
	// Failures counts the closes that failed with a code no retry changes;
	// LastCode is the code the last of them failed with.
	Failures int            `json:"failures,omitempty"`
	LastCode core.ErrorCode `json:"last_code,omitempty"`
}

// CloseAbandoned is the record of a run whose reservation the QA stage
// stopped trying to close: every close failed with a code no retry
// changes, such as a dispatcher's record that is missing or names another
// run. The reservation stays as it is for a person to settle.
type CloseAbandoned struct {
	RunID       core.RunID     `json:"run_id"`
	Code        core.ErrorCode `json:"code"`
	Reason      string         `json:"reason"`
	Failures    int            `json:"failures"`
	AbandonedAt time.Time      `json:"abandoned_at"`
}

// putCloseDue records, with the verdict that ends a run's coding, that its
// reservation is to be closed; the QA stage tries until it succeeds or the
// close is shown never to succeed.
func putCloseDue(ctx context.Context, tx state.Tx, run core.RunID) error {
	return put(ctx, tx, closeDuePrefix+string(run), closeDue{RunID: run})
}

// readCloseDue reads a close-due entry in either of its formats. The run is
// the one the key names, whatever the value says.
func readCloseDue(entry state.KVEntry) closeDue {
	run := core.RunID(strings.TrimPrefix(entry.Key, closeDuePrefix))
	var due closeDue
	if json.Unmarshal(entry.Value, &due) != nil {
		due = closeDue{}
	}
	due.RunID = run
	return due
}

// closeNeverSucceeds reports whether a close that failed with err fails the
// same way however often it is tried: what it reads is missing, does not
// hold together or belongs to another run.
func closeNeverSucceeds(err error) bool {
	return core.HasCode(err, core.CodeNotFound) || core.HasCode(err, core.CodeTampered) || core.HasCode(err, core.CodeInvalidArgument)
}

// closeDueRuns closes the reservation of every run whose close is due, the
// ones a verdict of this pass recorded and those whose close failed before.
// A run is forgotten in the transaction that closes it; one that fails is
// tried again on the next pass, and one whose close failed closeDueBound
// times with a code no retry changes is recorded as abandoned and reported
// once, in the pass that gives up on it.
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
		record := readCloseDue(entry)
		err := s.closeRun(ctx, record.RunID, entry.Version)
		switch {
		case err == nil:
			continue
		case !closeNeverSucceeds(err):
			failed = append(failed, fmt.Errorf("%s: close the run's reservation: %w", record.RunID, err))
			continue
		}
		abandoned, countErr := s.countFailedClose(ctx, entry, record, err)
		switch {
		case countErr != nil:
			failed = append(failed, fmt.Errorf("%s: close the run's reservation: %w; count the failure: %v", record.RunID, err, countErr))
		case abandoned:
			failed = append(failed, fmt.Errorf("%s: the run's reservation could not be closed in %d tries and is no longer tried; "+
				"it is recorded under %s%s for a person to settle: %w", record.RunID, closeDueBound, closeAbandonedPrefix, record.RunID, err))
		default:
			failed = append(failed, fmt.Errorf("%s: close the run's reservation (%d of %d tries): %w", record.RunID, record.Failures+1, closeDueBound, err))
		}
	}
	return failed
}

// countFailedClose counts a close of the run that failed with closeErr,
// a code no retry changes, on its close-due entry. At closeDueBound it
// replaces the entry with the run's abandoned record, in one transaction,
// and reports abandoned. An entry that changed since it was listed is left
// for the next pass.
func (s *QAStage) countFailedClose(ctx context.Context, entry state.KVEntry, record closeDue, closeErr error) (abandoned bool, err error) {
	record.Failures++
	record.LastCode = core.CodeOf(closeErr)
	err = s.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		if record.Failures < closeDueBound {
			raw, err := json.Marshal(record)
			if err != nil {
				return core.NewError(core.CodeInternal, "a close-due record cannot be encoded").WithCause(err)
			}
			_, err = tx.PutKV(ctx, state.KVEntry{Namespace: Namespace, Key: entry.Key, Value: raw, Version: entry.Version})
			return err
		}
		if err := tx.DeleteKV(ctx, Namespace, entry.Key, entry.Version); err != nil {
			return err
		}
		abandoned = true
		return put(ctx, tx, closeAbandonedPrefix+string(record.RunID), CloseAbandoned{
			RunID: record.RunID, Code: record.LastCode, Reason: closeErr.Error(), Failures: record.Failures, AbandonedAt: s.Clock.Now().UTC(),
		})
	})
	if err != nil {
		return false, err
	}
	return abandoned, nil
}

// ReadCloseAbandoned reads the record of a run whose reservation the QA
// stage stopped trying to close; NOT_FOUND when there is none.
func ReadCloseAbandoned(ctx context.Context, kv state.KV, run core.RunID) (CloseAbandoned, error) {
	var a CloseAbandoned
	return a, get(ctx, kv, closeAbandonedPrefix+string(run), &a)
}
