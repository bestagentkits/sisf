package release

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/bestagentkits/sisf/internal/catalog"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state"
)

// FinishDue is what a verified release still owes before its issue
// closes: the publication of its changelog and catalog, the landing of its
// change and the closing step. It is written in the transaction of the
// Verified step and deleted in the transaction of the closing step, so a
// Verified record always says whether it is still to be finished, whatever
// failed or stopped in between.
type FinishDue struct {
	IssueID core.IssueRef      `json:"issue_id"`
	RunID   core.RunID         `json:"run_id"`
	Release core.ReleaseDigest `json:"release"`
	Unit    string             `json:"unit"`
	// Published is set once the changelog and the catalog were published,
	// with PublicationRef the reference the Publisher returned.
	Published      bool   `json:"published,omitempty"`
	PublicationRef string `json:"publication_ref,omitempty"`
}

// FinishDueKey is the key, in RecoveryNamespace, of what the verified
// release of the run still owes.
func FinishDueKey(run core.RunID) string { return "finish-due/" + string(run) }

// ReadFinishDue reads what the verified release of run still owes;
// NOT_FOUND when nothing is recorded as due.
func ReadFinishDue(ctx context.Context, tx state.Tx, run core.RunID) (FinishDue, error) {
	entry, err := tx.GetKV(ctx, RecoveryNamespace, FinishDueKey(run))
	if err != nil {
		return FinishDue{}, err
	}
	var due FinishDue
	if err := json.Unmarshal(entry.Value, &due); err != nil || due.RunID != run {
		return FinishDue{}, core.NewError(core.CodeTampered, "what the verified release of "+string(run)+" owes cannot be read")
	}
	return due, nil
}

func putFinishDue(ctx context.Context, tx state.Tx, due FinishDue) error {
	raw, err := json.Marshal(due)
	if err != nil {
		return invalid("the finish due cannot be encoded").WithCause(err)
	}
	version := int64(0)
	if existing, err := tx.GetKV(ctx, RecoveryNamespace, FinishDueKey(due.RunID)); err == nil {
		version = existing.Version
	} else if !core.HasCode(err, core.CodeNotFound) {
		return err
	}
	_, err = tx.PutKV(ctx, state.KVEntry{Namespace: RecoveryNamespace, Key: FinishDueKey(due.RunID), Value: raw, Version: version})
	return err
}

func deleteFinishDue(ctx context.Context, tx state.Tx, run core.RunID) error {
	existing, err := tx.GetKV(ctx, RecoveryNamespace, FinishDueKey(run))
	if core.HasCode(err, core.CodeNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return tx.DeleteKV(ctx, RecoveryNamespace, FinishDueKey(run), existing.Version)
}

// What finishing a verified release waits on, in words safe to show on the
// issue: fixed text and an error code, never a message from GitHub.
const (
	waitsEmergencyStop = "an emergency stop holds publishing and merging; the verified release is finished once the stop is lifted"
	waitsLKG           = "the verified release could not be recorded as the last known good release (%s); this is tried again on every pass"
	waitsPublication   = "publishing the changelog and the feature catalog failed (%s); this is tried again on every pass"
	waitsLanding       = "merging the change could not be decided (%s); this is tried again on every pass"
	waitsClose         = "the issue could not be closed yet (%s); this is tried again on every pass"
	waitsLease         = "another holder has the issue (%s); this is tried again on every pass"
)

// waits words what finishing waits on with the code of err.
func waits(format string, err error) string { return fmt.Sprintf(format, core.CodeOf(err)) }

// finishing is a verified release on its way to Closed.
type finishing struct {
	h                *holding
	due              FinishDue
	feature, summary string
	candidate        qa.Candidate
	receipt          core.QAReceipt
	evidence         []string
}

// complete publishes, lands and closes a verified release, each step at
// most once: the publication is recorded as done before the landing, the
// Lander decides a landing once, and the closing step deletes what was
// due. Any step that cannot be taken now leaves the issue Verified with
// its finish due, and the outcome says what it waits on.
func (p *Pipeline) complete(ctx context.Context, f *finishing) (Outcome, error) {
	out := Outcome{Release: f.due.Release, State: f.h.record.State, PublicationRef: f.due.PublicationRef}
	held := func(err error, waitsOn string) (Outcome, error) {
		out.Result, out.Reason, out.State, out.Waits = ResultHeld, err.Error(), f.h.record.State, waitsOn
		return out, err
	}
	if p.frozen(ctx) {
		return held(core.NewError(core.CodeFrozen, "an emergency stop froze publishing; the verified release is not published"), waitsEmergencyStop)
	}
	if !f.due.Published {
		ref, err := p.cfg.Publisher.Publish(ctx, catalog.Entry{
			IssueID: f.due.IssueID, RunID: f.due.RunID, Unit: f.due.Unit, Release: f.due.Release,
			Feature: f.feature, Summary: f.summary, VerifiedAt: p.cfg.Clock.Now().UTC(),
		})
		if err != nil {
			return held(err, waits(waitsPublication, err))
		}
		f.due.Published, f.due.PublicationRef = true, ref
		if err := p.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error { return putFinishDue(ctx, tx, f.due) }); err != nil {
			return held(err, waits(waitsPublication, err))
		}
		out.PublicationRef = ref
	}
	landing, err := p.land(ctx, LandRequest{IssueID: f.due.IssueID, RunID: f.due.RunID, Release: f.due.Release,
		Candidate: f.candidate, Receipt: f.receipt})
	switch {
	case core.HasCode(err, core.CodeFrozen):
		return held(err, waitsEmergencyStop)
	case err != nil:
		return held(err, waits(waitsLanding, err))
	}
	evidence := f.evidence
	if f.due.PublicationRef != "" {
		evidence = append(evidence, f.due.PublicationRef)
	}
	if err := p.closeLanded(ctx, f.h, landing, evidence); err != nil {
		return held(err, waits(waitsClose, err))
	}
	out.Result, out.State = ResultClosed, f.h.record.State
	return out, p.audit(ctx, f.h, "release.promote", core.OutcomeExecuted, map[string]string{
		"release": string(f.due.Release), "publication": f.due.PublicationRef, "merged": strconv.FormatBool(landing.Merged),
	})
}

// FinishVerified finishes the release of an issue left Verified: the
// supervisor restarted, an emergency stop or an outage held it, or the LKG
// could not be recorded, after the release was verified. req is the
// release request of the issue's run, as the supervisor's records rebuild
// it. Whatever already happened is not done again: the LKG is recorded only
// when it does not name the release and no later release replaced it, and
// then only after req is verified once more; the publication is skipped
// once recorded; the change is merged at most once. A record an earlier
// build left Verified with nothing recorded as due is finished the same
// way, from the publication on (or the landing, when that build recorded
// it as due).
func (p *Pipeline) FinishVerified(ctx context.Context, req Request) (Outcome, error) {
	out := Outcome{Release: req.Artifact.Digest}
	stop := func(result Result, err error, waitsOn string) (Outcome, error) {
		out.Result, out.Reason, out.Waits = result, err.Error(), waitsOn
		return out, err
	}
	if err := req.validate(); err != nil {
		return out, err
	}
	done, ok := p.drive(req.IssueID)
	if !ok {
		return out, core.NewError(core.CodeLeaseHeld, "the issue is being released or recovered already")
	}
	defer done()
	if p.frozen(ctx) {
		out.State = core.StateVerified
		return stop(ResultHeld, core.NewError(core.CodeFrozen, "an emergency stop froze publishing"), waitsEmergencyStop)
	}
	h, err := p.take(ctx, req.IssueID)
	if err != nil {
		out.State = core.StateVerified
		return stop(ResultHeld, err, waits(waitsLease, err))
	}
	defer p.letGo(ctx, h)
	out.State = h.record.State
	if h.record.State != core.StateVerified || h.record.RunID != req.RunID || h.record.ReleaseDigest != req.Artifact.Digest {
		return stop(ResultRefused, core.NewError(core.CodeConflict, "the issue is not verified with this run's release"), "")
	}
	due, err := p.readDue(ctx, h.record, req.Artifact.Unit)
	if err != nil {
		return stop(ResultHeld, err, waits(waitsClose, err))
	}
	if err := p.ensureLKG(ctx, h.record, req); err != nil {
		return stop(ResultHeld, err, waits(waitsLKG, err))
	}
	return p.complete(ctx, &finishing{h: h, due: due, feature: req.Feature, summary: req.Summary, candidate: req.Candidate,
		receipt: req.Receipt, evidence: []string{"release:" + string(req.Artifact.Digest)}})
}

// RecordVerifiedLKG makes the release of an issue left Verified the LKG
// of its unit when it is not yet and no later release replaced it, so a
// supervisor starting again serves what the record says. It runs before
// the LKG is adopted at startup; it writes no lifecycle step.
func (p *Pipeline) RecordVerifiedLKG(ctx context.Context, req Request) error {
	if err := req.validate(); err != nil {
		return err
	}
	done, ok := p.drive(req.IssueID)
	if !ok {
		return core.NewError(core.CodeLeaseHeld, "the issue is being released or recovered already")
	}
	defer done()
	var record core.StateRecord
	if err := p.cfg.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		record, err = tx.GetRecord(ctx, req.IssueID)
		return err
	}); err != nil {
		return err
	}
	if record.State != core.StateVerified || record.RunID != req.RunID || record.ReleaseDigest != req.Artifact.Digest {
		return core.NewError(core.CodeConflict, "the issue is not verified with this run's release")
	}
	return p.ensureLKG(ctx, record, req)
}

// readDue reads what the verified release of record still owes. A record
// an earlier build left with only its landing due was published already;
// one with nothing due is owed everything from the publication on.
func (p *Pipeline) readDue(ctx context.Context, record core.StateRecord, unit string) (FinishDue, error) {
	due := FinishDue{IssueID: record.IssueID, RunID: record.RunID, Release: record.ReleaseDigest, Unit: unit}
	err := p.cfg.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		stored, err := ReadFinishDue(ctx, tx, record.RunID)
		switch {
		case err == nil:
			due = stored
			return nil
		case !core.HasCode(err, core.CodeNotFound):
			return err
		}
		_, err = ReadLandingDue(ctx, tx, record.RunID)
		switch {
		case err == nil:
			due.Published = true
			return nil
		case core.HasCode(err, core.CodeNotFound):
			return nil
		}
		return err
	})
	if due.IssueID != record.IssueID || due.Release != record.ReleaseDigest {
		return FinishDue{}, core.NewError(core.CodeTampered, "what the verified release of "+string(record.RunID)+" owes names another release")
	}
	return due, err
}

// ensureLKG makes sure the LKG of the release's unit is the verified
// release of record, unless a release verified after it replaced it. An
// entry is written only for a request verified again now: only the
// pipeline's verification may make an artifact the LKG.
func (p *Pipeline) ensureLKG(ctx context.Context, record core.StateRecord, req Request) error {
	lkg, found, err := p.cfg.LKG.Get(ctx, req.Artifact.Unit)
	if err != nil {
		return err
	}
	if found && lkg.Source == SourcePromoted {
		if lkg.Digest == record.ReleaseDigest && lkg.RunID == record.RunID {
			return nil
		}
		if lkg.RecordedAt.After(record.UpdatedAt) {
			return nil // a later release is the LKG now
		}
	}
	v, err := p.verify(ctx, req, record)
	if err != nil {
		return err
	}
	return p.cfg.LKG.record(v)
}
