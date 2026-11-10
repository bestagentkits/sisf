package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// DefaultLeaseTTL is how long the exporter holds an issue while it works.
const DefaultLeaseTTL = 2 * time.Minute

// Exporter hands the patch of every run whose coding attempt succeeded to
// the supervisor, and sends a run whose candidate failed evaluation back
// to its coding agent. It works on an issue only under the issue's lease.
type Exporter struct {
	// Owner names this exporter in the leases it takes.
	Owner     string
	State     state.Store
	Artifacts ArtifactSource
	Clock     core.Clock
	LeaseTTL  time.Duration
}

// PassResult is what one pass did.
type PassResult struct {
	Exported []core.IssueRef
	// Retried are issues sent back to the coding agent after a failed evaluation.
	Retried []core.IssueRef
	// Unreadable are issues whose run left no patch that could be read; the
	// coding agent gets another attempt within the run's limit.
	Unreadable []core.IssueRef
	Errors     []error
}

// work is one issue the pass acts on.
type work struct {
	record core.StateRecord
	run    dispatcher.RunRecord
	retry  *handoff.PatchDoc
}

var errAlreadyHandled = core.NewError(core.CodeConflict, "the issue moved on before the exporter acted")

// Pass looks at every issue in Developing whose coding attempt succeeded.
func (e *Exporter) Pass(ctx context.Context) PassResult {
	var res PassResult
	var todo []work
	err := e.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		records, err := tx.ListRecords(ctx, state.RecordFilter{States: []core.State{core.StateDeveloping}})
		for _, record := range records {
			w, ok, err := pending(ctx, tx, record)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Errorf("%s: %w", record.IssueID, err))
			} else if ok {
				todo = append(todo, w)
			}
		}
		return err
	})
	if err != nil {
		res.Errors = append(res.Errors, err)
		return res
	}
	for _, w := range todo {
		var done *[]core.IssueRef
		var err error
		if w.retry != nil {
			done, err = &res.Retried, e.reopen(ctx, w, retryDecision(*w.retry), nil)
		} else {
			done, err = e.export(ctx, w, &res)
		}
		switch {
		case err == nil:
			*done = append(*done, w.record.IssueID)
		case core.HasCode(err, core.CodeLeaseHeld) || errors.Is(err, errAlreadyHandled):
		default:
			res.Errors = append(res.Errors, fmt.Errorf("%s: %w", w.record.IssueID, err))
		}
	}
	return res
}

// pending decides what the pass does with one record. A run whose agent
// attempt succeeded either left a new patch to export, or left the patch
// the evaluator already failed: the record's attempt moved on and the patch
// of the attempt before came from this very agent attempt.
func pending(ctx context.Context, tx state.Tx, record core.StateRecord) (work, bool, error) {
	run, err := ReadRunRecord(ctx, tx, record.RunID)
	if core.HasCode(err, core.CodeNotFound) {
		return work{}, false, nil
	}
	if err != nil || run.Status != dispatcher.RunSucceeded {
		return work{}, false, err
	}
	if _, err := handoff.ReadPatch(ctx, tx, record.RunID, record.Attempt); err == nil || !core.HasCode(err, core.CodeNotFound) {
		return work{}, false, err
	}
	w := work{record: record, run: run}
	if record.Attempt > 1 {
		prev, err := handoff.ReadPatch(ctx, tx, record.RunID, record.Attempt-1)
		switch {
		case err == nil && prev.AgentAttempt >= run.Attempt:
			w.retry = &prev
		case err != nil && !core.HasCode(err, core.CodeNotFound):
			return work{}, false, err
		}
	}
	return w, true, nil
}

// export reads the run's patch and records it for the supervisor.
func (e *Exporter) export(ctx context.Context, w work, res *PassResult) (*[]core.IssueRef, error) {
	i := slices.IndexFunc(w.run.Artifacts, func(a agent.Artifact) bool { return a.Kind == agent.ArtifactPatch })
	var diff []byte
	var err error = core.NewError(core.CodeNotFound, "the run left no patch")
	if i >= 0 {
		diff, err = e.Artifacts.Content(ctx, w.run.Artifacts[i])
	}
	if err != nil {
		if core.HasCode(err, core.CodeUnavailable) {
			return nil, err
		}
		failure := &dispatcher.AttemptFailure{Attempt: w.run.Attempt, Code: core.CodeOf(err), Detail: err.Error()}
		return &res.Unreadable, e.reopen(ctx, w, fmt.Sprintf("Attempt %d left no patch that could be read.", w.run.Attempt), failure)
	}
	identity := "agent:unknown/coder"
	if w.run.Agent != nil {
		identity = CoderIdentity(w.run.Agent.Backend)
	}
	doc := handoff.PatchDoc{
		IssueID: w.record.IssueID, RunID: w.record.RunID, Attempt: w.record.Attempt, AgentAttempt: w.run.Attempt,
		Diff: string(diff), Digest: w.run.Artifacts[i].Digest, CoderIdentity: identity, Policy: w.run.Brief.Policy,
		Summary: summaryOf(w.run), ExportedAt: e.Clock.Now().UTC(),
	}
	return &res.Exported, e.underLease(ctx, w, func(ctx context.Context, tx state.Tx) error {
		return handoff.PutDoc(ctx, tx, handoff.Namespace, handoff.PatchKey(doc.RunID, doc.Attempt), doc)
	})
}

// retryDecision is what the next coding attempt is told about the last one.
func retryDecision(prev handoff.PatchDoc) string {
	return fmt.Sprintf("Attempt %d did not pass independent QA; fix what the evaluator found before submitting again.", prev.Attempt)
}

// reopen marks the run working again with no agent and adds the decision
// to its brief: the dispatcher resumes a working run no lease covers and
// starts its next attempt, within the run's attempt limit.
func (e *Exporter) reopen(ctx context.Context, w work, decision string, failure *dispatcher.AttemptFailure) error {
	return e.underLease(ctx, w, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, handoff.Namespace, "run/"+string(w.record.RunID))
		if err != nil {
			return err
		}
		var run dispatcher.RunRecord
		if err := json.Unmarshal(entry.Value, &run); err != nil || run.RunID != w.record.RunID || run.Status != dispatcher.RunSucceeded {
			return errAlreadyHandled
		}
		now := e.Clock.Now()
		if failure != nil {
			failure.At = now
			run.Failures = append(run.Failures, *failure)
		}
		run.Status, run.Agent, run.Artifacts, run.AttemptStartedAt, run.UpdatedAt = dispatcher.RunWorking, nil, nil, now, now
		run.Brief.Handoff.Decisions = append(run.Brief.Handoff.Decisions, decision)
		raw, err := json.Marshal(run)
		if err != nil {
			return core.NewError(core.CodeInternal, "a run record cannot be encoded").WithCause(err)
		}
		_, err = tx.PutKV(ctx, state.KVEntry{Namespace: handoff.Namespace, Key: entry.Key, Value: raw, Version: entry.Version})
		return err
	})
}

// underLease takes the issue lease, checks the record has not moved, runs
// fn and gives the lease up, all in one transaction.
func (e *Exporter) underLease(ctx context.Context, w work, fn func(context.Context, state.Tx) error) error {
	ttl := e.LeaseTTL
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	return e.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		lease, err := tx.AcquireLease(ctx, state.IssueLeaseResource(w.record.IssueID), e.Owner, ttl)
		if err != nil {
			return err
		}
		record, err := tx.GetRecord(ctx, w.record.IssueID)
		if err != nil {
			return err
		}
		if record.State != core.StateDeveloping || record.RunID != w.record.RunID || record.Attempt != w.record.Attempt {
			return errAlreadyHandled
		}
		if err := fn(ctx, tx); err != nil {
			return err
		}
		return tx.ReleaseLease(ctx, lease)
	})
}

// ReadRunRecord reads the dispatcher's record of a run.
func ReadRunRecord(ctx context.Context, kv state.KV, run core.RunID) (dispatcher.RunRecord, error) {
	entry, err := kv.GetKV(ctx, handoff.Namespace, "run/"+string(run))
	if err != nil {
		return dispatcher.RunRecord{}, err
	}
	var record dispatcher.RunRecord
	if err := json.Unmarshal(entry.Value, &record); err != nil || record.RunID != run {
		return dispatcher.RunRecord{}, core.NewError(core.CodeTampered, "the stored record of run "+string(run)+" cannot be read")
	}
	return record, nil
}

// CoderIdentity is the agent identity of the coding agent of a backend.
func CoderIdentity(backend string) string { return handoff.CoderIdentity(backend) }

// summaryOf is the changelog line of a run: the first line of its scope.
func summaryOf(run dispatcher.RunRecord) string {
	line, _, _ := strings.Cut(strings.TrimSpace(run.Brief.Handoff.SoW), "\n")
	line = strings.TrimSpace(strings.TrimLeft(line, "# "))
	if len(line) > 120 {
		line = line[:120]
	}
	if line == "" {
		line = "Change for " + run.Issue.String()
	}
	return line
}
