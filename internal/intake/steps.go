package intake

import (
	"context"
	"fmt"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// plan decides the next step for an issue, or nil when there is none. It
// writes nothing to the operational store itself; apply does.
func (s *Service) plan(ctx context.Context, in input, snap snapshot, resume bool) (*plan, error) {
	if !snap.exists {
		doc := snap.doc
		doc.AgentPreference = in.labels.AgentPreference
		return s.step(snap, doc, core.EventIssueReceived, "the issue asked for intake")
	}
	switch snap.record.State {
	case core.StateIntake:
		draft := Analyze(in.issue, in.comments, in.labels)
		if draft.Complete() {
			return s.scoped(in, snap, draft, core.EventInfoSufficient)
		}
		return s.ask(in, snap, draft, true)
	case core.StateClarifying:
		return s.planClarifying(in, snap, resume)
	case core.StateScopeDraft:
		return s.planScope(ctx, in, snap)
	case core.StateApprovalPending:
		return s.planApproval(ctx, in, snap)
	}
	return nil, nil
}

// step builds a plan around one lifecycle transition. The target state is
// read from the transition table, so intake cannot take a step the
// lifecycle does not have. Each of its events happens at most once per
// issue, which makes the event a sufficient idempotency key.
func (s *Service) step(snap snapshot, doc issueDoc, event core.Event, reason string, evidence ...string) (*plan, error) {
	var from core.State
	if snap.exists {
		from = snap.record.State
	}
	to, err := statemachine.Next(from, event)
	if err != nil {
		return nil, err
	}
	return &plan{doc: doc, transition: &core.Transition{
		IssueID: doc.Issue, From: from, To: to, Event: event, ExpectedVersion: snap.record.Version,
		IdempotencyKey: "intake/" + doc.Issue.String() + "/" + string(event), Attempt: snap.record.Attempt,
		ActorID: serviceActor, Reason: reason, EvidenceRefs: evidence,
	}}, nil
}

// with adds effects to a plan, passing an earlier error through.
func (p *plan) with(effects ...func() (state.OutboxMessage, error)) (*plan, error) {
	for _, build := range effects {
		m, err := build()
		if err != nil {
			return nil, err
		}
		p.effects = append(p.effects, m)
	}
	return p, nil
}

// scoped writes the first scope version from a complete draft and moves the
// issue to ScopeDraft.
func (s *Service) scoped(in input, snap snapshot, draft Draft, event core.Event) (*plan, error) {
	kind := Classify(draft, in.issue.AuthorAssociation, in.policy.Catalog).Kind
	sow := newSoW(snap.doc.Issue, snap.doc.SoWVersion+1, kind, draft, in.policy.Revision, in.now)
	doc := snap.doc
	doc.SoWVersion, doc.SoWDigest = sow.Version, sow.Digest
	// Whatever was asked is answered; only the numbering of comments stays.
	doc.Clarify = clarifyState{Asked: doc.Clarify.Asked}
	p, err := s.step(snap, doc, event, "the request says what to change and how to tell it is done", sowRef(sow))
	if err != nil {
		return nil, err
	}
	p.sow = &sow
	return p.with(func() (state.OutboxMessage, error) {
		return commentEffect(sow.Issue, fmt.Sprintf("sow/%d", sow.Version), sowComment(sow))
	})
}

// sowRef names a scope version in evidence references.
func sowRef(sow SoW) string { return fmt.Sprintf("sow:v%d@%s", sow.Version, sow.Digest) }

// ask posts the questions for what a draft lacks and starts the wait. From
// Intake this is the step to Clarifying; in Clarifying it is another round.
func (s *Service) ask(in input, snap snapshot, draft Draft, fromIntake bool) (*plan, error) {
	timeout := in.policy.Manifest.Limits.ClarifyTimeout.Std()
	if timeout <= 0 {
		timeout = defaultClarifyTimeout
	}
	doc := snap.doc
	doc.Clarify.Round++
	doc.Clarify.Asked++
	doc.Clarify.Deadline = in.now.Add(timeout)
	doc.Clarify.SeenReply = latestReply(in.issue, in.comments)
	doc.Clarify.NeedsInfo, doc.Clarify.LabelPending = false, false
	p := &plan{doc: doc}
	if fromIntake {
		var err error
		if p, err = s.step(snap, doc, core.EventInfoMissing, "the request lacks information"); err != nil {
			return nil, err
		}
	}
	return p.with(func() (state.OutboxMessage, error) {
		return commentEffect(doc.Issue, fmt.Sprintf("clarify/%d", doc.Clarify.Asked), clarifyComment(draft.Missing, doc.Clarify.Deadline))
	})
}

// park stops the wait: the issue gets sisf:needs-info and one last comment,
// and from then on intake neither asks, reads nor spends anything for it.
func park(snap snapshot, why string) (*plan, error) {
	doc := snap.doc
	doc.Clarify.NeedsInfo, doc.Clarify.LabelPending = true, true
	return (&plan{doc: doc}).with(
		func() (state.OutboxMessage, error) {
			return labelEffect(doc.Issue, parkLabelName(doc.Clarify.Asked), []string{github.LabelNeedsInfo}, nil)
		},
		func() (state.OutboxMessage, error) {
			return commentEffect(doc.Issue, fmt.Sprintf("needs-info/%d", doc.Clarify.Asked), parkedComment(why))
		},
	)
}

// planClarifying decides what an issue that was asked questions does next.
//
// A complete answer moves it on, even one that arrives late. Otherwise the
// issue is parked once the wait is over or the rounds are used up. A new
// reply that still leaves a gap gets one more round; no new reply gets
// nothing, so repeated events cannot make intake ask again.
func (s *Service) planClarifying(in input, snap snapshot, resume bool) (*plan, error) {
	parked := snap.doc.Clarify.NeedsInfo
	if parked && !resume {
		return nil, nil
	}
	draft := Analyze(in.issue, in.comments, in.labels)
	switch {
	case draft.Complete():
		return s.scoped(in, snap, draft, core.EventClarificationCompleted)
	case parked:
		// A person removed sisf:needs-info: start a fresh wait.
		snap.doc.Clarify.Round = 0
		return s.ask(in, snap, draft, false)
	case !in.now.Before(snap.doc.Clarify.Deadline):
		return park(snap, "No complete answer arrived in time.")
	case latestReply(in.issue, in.comments) <= snap.doc.Clarify.SeenReply:
		return nil, nil
	case snap.doc.Clarify.Round >= MaxClarifyRounds:
		return park(snap, fmt.Sprintf("The answers given in %d rounds did not complete the request.", MaxClarifyRounds))
	}
	return s.ask(in, snap, draft, false)
}

// ExpireClarifications parks every issue whose wait for an answer is over
// and returns them. It is what a timer calls. It reads only the operational
// store: an issue that nobody answers costs no GitHub call and no model
// call, however long it stays open.
func (s *Service) ExpireClarifications(ctx context.Context) ([]core.IssueRef, error) {
	var waiting []core.StateRecord
	err := s.cfg.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		waiting, err = tx.ListRecords(ctx, state.RecordFilter{States: []core.State{core.StateClarifying}})
		return err
	})
	if err != nil {
		return nil, err
	}
	now := s.cfg.Clock.Now()
	var parked []core.IssueRef
	for _, record := range waiting {
		snap, err := s.load(ctx, record.IssueID)
		if err != nil {
			return parked, err
		}
		if snap.record.State != core.StateClarifying || snap.doc.Clarify.NeedsInfo || now.Before(snap.doc.Clarify.Deadline) {
			continue
		}
		p, err := park(snap, "No complete answer arrived in time.")
		if err != nil {
			return parked, err
		}
		switch err := s.apply(ctx, snap, p); {
		case core.HasCode(err, core.CodeConflict):
			// The issue moved while it was being parked; the next tick looks again.
		case err != nil:
			return parked, err
		default:
			parked = append(parked, record.IssueID)
		}
	}
	return parked, nil
}
