package release

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Where the recovery report of a rolled-back release is kept and announced.
const (
	// RecoveryNamespace is the control-zone document namespace of the reports.
	RecoveryNamespace = "release"
	// TopicRecoveryReport is the outbox topic that posts a report on the issue.
	TopicRecoveryReport = "release.recovery_report"
)

// RecoveryReport is the evidence of a release that was restored to the LKG.
type RecoveryReport struct {
	IssueID    core.IssueRef      `json:"issue_id"`
	RunID      core.RunID         `json:"run_id"`
	Release    core.ReleaseDigest `json:"release"`
	RestoredTo core.ReleaseDigest `json:"restored_to"`
	Reason     string             `json:"reason"`
	// Observed is what runs after the restore, read from the running system.
	Observed deploy.Observed `json:"observed"`
	Windows  []WindowReport  `json:"windows,omitempty"`
	// DataUntouched states the restore replaced code and configuration only;
	// no data volume or stored data was overwritten.
	DataUntouched bool      `json:"data_untouched"`
	At            time.Time `json:"at"`
}

// RecoveryKey is the document key of a run's recovery report.
func RecoveryKey(run core.RunID) string { return "recovery/" + string(run) }

// restore switches the unit back to the LKG, proves from the running system
// that the LKG serves again, then writes RolledBack and, with the recovery
// report, Blocked. Only code and configuration are switched: the backend's
// Rollback never touches data volumes, and nothing here restores data.
func (p *Pipeline) restore(ctx context.Context, r *rollout, reason string) (Outcome, error) {
	out := Outcome{
		Release: r.rev.Digest, Windows: r.windows, Reason: reason, State: r.h.record.State, Observed: r.observed,
	}
	failed := func(result Result, err error) (Outcome, error) {
		out.Result = result
		_ = p.audit(ctx, r.h, "release.restore", core.OutcomeFailed, map[string]string{"reason": reason})
		return out, err
	}
	if err := p.cfg.Backend.Rollback(ctx, r.lkgRev); err != nil {
		return failed(ResultRestoreFailed, holdf("the last known good release could not be restored").WithCause(err))
	}
	observed, err := p.cfg.Backend.ObservedRevision(ctx, r.unit)
	if err != nil || !observed.Matches(r.lkgRev) {
		return failed(ResultRestoreFailed, holdf("the last known good release is not observed running and healthy after the restore"))
	}
	out.Observed, out.RestoredTo = observed, r.lkgRev.Digest
	event := core.EventRegressionDetected
	if r.h.record.State == core.StateCanary {
		event = core.EventErrorThresholdExceeded
	}
	ref := "kv:" + RecoveryNamespace + "/" + RecoveryKey(r.h.record.RunID)
	evidence := []string{"observed:" + string(observed.Digest), ref}
	if err := p.step(ctx, r.h, stepSpec{event: event, reason: reason, evidence: evidence}); err != nil {
		return failed(ResultHeld, err)
	}
	report := RecoveryReport{
		IssueID: r.h.record.IssueID, RunID: r.h.record.RunID, Release: r.rev.Digest, RestoredTo: r.lkgRev.Digest,
		Reason: reason, Observed: observed, Windows: r.windows, DataUntouched: true, At: p.cfg.Clock.Now().UTC(),
	}
	err = p.step(ctx, r.h, stepSpec{
		event: core.EventRecoveryReported, reason: "the LKG serves again; the recovery report is recorded", evidence: evidence,
		also: func(ctx context.Context, tx state.Tx, _ core.StateRecord) error {
			return writeRecoveryReport(ctx, tx, report)
		},
	})
	out.State = r.h.record.State
	if err != nil {
		return failed(ResultHeld, err)
	}
	out.Result = ResultBlocked
	if err := p.audit(ctx, r.h, "release.rollback", core.OutcomeExecuted, map[string]string{
		"release": string(r.rev.Digest), "restored_to": string(r.lkgRev.Digest), "reason": reason,
	}); err != nil {
		return out, err
	}
	return out, nil
}

// writeRecoveryReport keeps the report as a document and queues it for the
// issue, in the transaction that blocks the issue.
func writeRecoveryReport(ctx context.Context, tx state.Tx, report RecoveryReport) error {
	raw, err := json.Marshal(report)
	if err != nil {
		return invalid("the recovery report cannot be encoded").WithCause(err)
	}
	key := RecoveryKey(report.RunID)
	version := int64(0)
	if existing, err := tx.GetKV(ctx, RecoveryNamespace, key); err == nil {
		version = existing.Version
	} else if !core.HasCode(err, core.CodeNotFound) {
		return err
	}
	if _, err := tx.PutKV(ctx, state.KVEntry{Namespace: RecoveryNamespace, Key: key, Value: raw, Version: version}); err != nil {
		return err
	}
	return tx.Enqueue(ctx, state.OutboxMessage{Topic: TopicRecoveryReport, Key: "release/" + string(report.RunID) + "/recovery", Payload: raw})
}
