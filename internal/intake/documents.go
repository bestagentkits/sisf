package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// namespace is where intake keeps its documents in the operational store.
const namespace = "intake"

// issueDoc is what intake remembers about one issue besides its lifecycle
// state: which scope version is current, what was asked and when, and which
// approval request the issue waits on.
type issueDoc struct {
	Issue core.IssueRef `json:"issue"`
	// SoWVersion is the latest scope version written; 0 means none yet.
	SoWVersion int         `json:"sow_version,omitempty"`
	SoWDigest  core.Digest `json:"sow_digest,omitempty"`
	// ApprovedVersion is the scope version the issue became Ready with.
	ApprovedVersion int `json:"approved_version,omitempty"`
	// ApprovalID is the approval request the current scope waits on.
	ApprovalID string `json:"approval_id,omitempty"`
	// NotedRequest is the request whose "changes requested" answer was
	// already passed on to the issue.
	NotedRequest string `json:"noted_request,omitempty"`
	// AgentPreference is the routing wish of an agent: label. It carries no
	// permission.
	AgentPreference string       `json:"agent_preference,omitempty"`
	Clarify         clarifyState `json:"clarify,omitzero"`
	// Denial is set when the issue was blocked because its scope did not get
	// the approval it needed.
	Denial *Denial `json:"denial,omitempty"`
}

// clarifyState tracks the questions asked on an issue.
type clarifyState struct {
	// Round counts the questions asked since the issue last started waiting.
	Round int `json:"round,omitempty"`
	// Asked counts every question ever asked; it numbers the comments.
	Asked int `json:"asked,omitempty"`
	// Deadline is when an unanswered question parks the issue.
	Deadline time.Time `json:"deadline,omitzero"`
	// SeenReply is the newest reply that was read when the last question
	// was asked. Only a newer reply can cause another question.
	SeenReply int64 `json:"seen_reply,omitempty"`
	// NeedsInfo says the issue is parked: nothing more is asked, read or
	// spent until a person removes the sisf:needs-info label.
	NeedsInfo bool `json:"needs_info,omitempty"`
}

func issueKey(issue core.IssueRef) string { return "issue/" + issue.String() }

func sowKey(issue core.IssueRef, version int) string {
	return fmt.Sprintf("sow/%s/%06d", issue.String(), version)
}

// snapshot is what intake knows about an issue at the start of a step.
type snapshot struct {
	// record is the lifecycle record; exists is false when there is none.
	record core.StateRecord
	exists bool
	doc    issueDoc
	// docVersion is the version the document was read at; 0 means absent.
	docVersion int64
	// sow is the current scope version, when one was written.
	sow *SoW
}

// load reads the record, the document and the current scope of an issue in
// one read-only transaction.
func (s *Service) load(ctx context.Context, issue core.IssueRef) (snapshot, error) {
	var snap snapshot
	err := s.cfg.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		snap = snapshot{doc: issueDoc{Issue: issue}}
		record, err := tx.GetRecord(ctx, issue)
		switch {
		case err == nil:
			snap.record, snap.exists = record, true
		case !core.HasCode(err, core.CodeNotFound):
			return err
		}
		entry, err := tx.GetKV(ctx, namespace, issueKey(issue))
		switch {
		case core.HasCode(err, core.CodeNotFound):
			return nil
		case err != nil:
			return err
		}
		snap.docVersion = entry.Version
		if err := decode(entry, &snap.doc); err != nil {
			return err
		}
		if snap.doc.SoWVersion == 0 {
			return nil
		}
		sow, err := readSoW(ctx, tx, issue, snap.doc.SoWVersion)
		if err != nil {
			return err
		}
		snap.sow = &sow
		return nil
	})
	return snap, err
}

// readSoW reads one stored scope version.
func readSoW(ctx context.Context, tx state.Tx, issue core.IssueRef, version int) (SoW, error) {
	entry, err := tx.GetKV(ctx, namespace, sowKey(issue, version))
	if err != nil {
		return SoW{}, err
	}
	var sow SoW
	return sow, decode(entry, &sow)
}

// decode reads a stored document. One that no longer parses was damaged
// outside intake and is reported, never guessed at.
func decode(entry state.KVEntry, into any) error {
	if err := json.Unmarshal(entry.Value, into); err != nil {
		return core.NewError(core.CodeTampered, "a stored intake document cannot be read").WithCause(err)
	}
	return nil
}

// put writes one document at the version it was read at; 0 creates it.
func put(ctx context.Context, tx state.Tx, key string, version int64, doc any) error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return core.NewError(core.CodeInternal, "an intake document cannot be encoded").WithCause(err)
	}
	_, err = tx.PutKV(ctx, state.KVEntry{Namespace: namespace, Key: key, Value: raw, Version: version})
	return err
}

// ApprovedScope returns the scope version an issue became Ready with: the
// exact text that was permitted or approved, which is what an agent works
// from. Later edits of the issue are not part of it. Before the issue is
// Ready the answer is APPROVAL_REQUIRED, and NOT_FOUND for an unknown issue.
func (s *Service) ApprovedScope(ctx context.Context, issue core.IssueRef) (SoW, error) {
	var sow SoW
	err := s.cfg.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, namespace, issueKey(issue))
		if err != nil {
			if core.HasCode(err, core.CodeNotFound) {
				return core.NewError(core.CodeNotFound, "intake has no record of this issue")
			}
			return err
		}
		var doc issueDoc
		if err := decode(entry, &doc); err != nil {
			return err
		}
		if doc.ApprovedVersion == 0 {
			return core.NewError(core.CodeApprovalRequired, "the scope of this issue has not been permitted or approved yet")
		}
		sow, err = readSoW(ctx, tx, issue, doc.ApprovedVersion)
		return err
	})
	if err != nil {
		return SoW{}, err
	}
	return sow, nil
}
