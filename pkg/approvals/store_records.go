package approvals

import (
	"context"
	"encoding/json"
	"math"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Where requests and decisions live in the operational store.
const (
	namespace     = "approvals"
	requestPrefix = "request/"
	// listAll asks for every entry whether the store reads a limit below one
	// as "no limit" or not.
	listAll = math.MaxInt32
	// writeAttempts bounds how often a write that lost a race is repeated.
	writeAttempts = 3
)

// Names of the audited actions.
const (
	actionRequest = "approvals.request"
	actionDecide  = "approvals.decide"
	actionConsume = "approvals.consume"
	// serviceActor is who the store acts as when no person is involved.
	serviceActor = "service:approvals"
)

func requestKey(id string) string  { return requestPrefix + id }
func decisionKey(id string) string { return "decision/" + id }
func nonceKey(nonce string) string { return "nonce/" + nonce }

// decode reads a stored document. A document that no longer parses was
// damaged outside the store and is reported, never guessed at.
func decode(entry state.KVEntry, into any) error {
	if err := json.Unmarshal(entry.Value, into); err != nil {
		return core.NewError(core.CodeTampered, "a stored approval record cannot be read").WithCause(err)
	}
	return nil
}

// put writes one document at the version that was read; 0 creates it.
func put(ctx context.Context, tx state.Tx, key string, version int64, doc any) error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return core.NewError(core.CodeInternal, "an approval record cannot be encoded").WithCause(err)
	}
	_, err = tx.PutKV(ctx, state.KVEntry{Namespace: namespace, Key: key, Value: raw, Version: version})
	return err
}

// absent returns ALREADY_EXISTS with message when the key holds a document.
func absent(ctx context.Context, tx state.Tx, key, message string) error {
	_, err := tx.GetKV(ctx, namespace, key)
	switch {
	case err == nil:
		return core.NewError(core.CodeAlreadyExists, message)
	case core.HasCode(err, core.CodeNotFound):
		return nil
	}
	return err
}

// loadRequest reads a request and the version it was stored at.
func loadRequest(ctx context.Context, tx state.Tx, id string) (Request, int64, error) {
	if id == "" {
		return Request{}, 0, invalid("an approval request id is needed")
	}
	entry, err := tx.GetKV(ctx, namespace, requestKey(id))
	if err != nil {
		if core.HasCode(err, core.CodeNotFound) {
			return Request{}, 0, core.NewError(core.CodeNotFound, "no such approval request")
		}
		return Request{}, 0, err
	}
	var r Request
	if err := decode(entry, &r); err != nil {
		return Request{}, 0, err
	}
	return r, entry.Version, nil
}

// loadDecision reads the decision recorded for a request.
func loadDecision(ctx context.Context, tx state.Tx, id string) (Approval, error) {
	entry, err := tx.GetKV(ctx, namespace, decisionKey(id))
	if err != nil {
		if core.HasCode(err, core.CodeNotFound) {
			return Approval{}, core.NewError(core.CodeNotFound, "the approval request has no decision yet")
		}
		return Approval{}, err
	}
	var a Approval
	if err := decode(entry, &a); err != nil {
		return Approval{}, err
	}
	return a, nil
}

// auditActor is who an audit record names.
type auditActor struct {
	id      string
	kind    core.ActorKind
	surface core.Surface
	details map[string]string
}

// service is the store acting for itself, on behalf of whoever asked.
func (s *KVStore) service(onBehalfOf string) auditActor {
	return auditActor{id: serviceActor, kind: core.ActorService, details: map[string]string{"requested_by": onBehalfOf}}
}

// record appends the audit record of one finished action on a request.
// failure is the refusal being recorded, or nil when the action took effect.
//
// A refusal is returned to the caller whether or not it could be audited. An
// action that took effect but could not be audited is reported as failed, so
// the caller does not act on an approval that left no trace.
func (s *KVStore) record(ctx context.Context, r Request, action string, actor auditActor, failure error) error {
	if r.ID == "" {
		// The request was never found, so there is nothing to bind a record to.
		return failure
	}
	outcome := core.OutcomeExecuted
	if failure != nil {
		outcome = core.OutcomeDenied
	}
	if err := s.append(ctx, r, action, actor, outcome, failure); err != nil && failure == nil {
		return core.NewError(core.CodeOf(err), "the action could not be audited, so it is not reported as done").WithCause(err)
	}
	return failure
}

// append writes one audit record about a request with the given outcome.
// cause is the error the record explains, if any.
func (s *KVStore) append(ctx context.Context, r Request, action string, actor auditActor, outcome core.AuditOutcome, cause error) error {
	event := core.AuditEvent{
		At: s.clock.Now(), FactoryID: r.FactoryID, ActorID: actor.id, ActorKind: actor.kind, Surface: actor.surface,
		Action: action, Resource: "approval:" + r.ID, SubjectDigest: r.SubjectDigest, PolicySHA: r.PolicyRevision,
		IssueID: r.IssueID, Outcome: outcome, Details: map[string]string{"request_action": r.Action},
	}
	for key, value := range actor.details {
		event.Details[key] = value
	}
	if cause != nil {
		event.Details["code"] = string(core.CodeOf(cause))
	}
	_, err := s.audit.Append(ctx, event)
	return err
}
