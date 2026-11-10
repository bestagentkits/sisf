package policy

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Where the baseline lives in the operational store, and the audited action.
const (
	baselineNamespace = "policy"
	baselineKey       = "baseline"
	// ActionApproveRevision is the action name of an approval request for a
	// new policy revision, and of the audit records of every attempt.
	ActionApproveRevision = "policy.approve_revision"
)

// Baseline is the policy revision the Owner approved, with who approved it
// and how.
type Baseline struct {
	Revision core.CommitSHA `json:"revision"`
	// Digest is the content of the foundation files at Revision.
	Digest     core.Digest `json:"digest"`
	ApprovedBy string      `json:"approved_by"`
	ApprovedAt time.Time   `json:"approved_at"`
	// ApprovalID is the approval that was spent on the change. It is empty
	// for the first baseline, set at install.
	ApprovalID string `json:"approval_id,omitempty"`
	// Previous is the revision this one replaced.
	Previous core.CommitSHA `json:"previous,omitempty"`
}

// BaselinesConfig is what the baseline record is built from. Every field
// but Clock is required.
type BaselinesConfig struct {
	State  state.Store
	Source Source
	// Approvals is where an approval of a new revision is spent.
	Approvals approvals.Store
	Audit     core.AuditLog
	// FactoryID is the factory this policy belongs to. A revision whose
	// manifest names another factory is not this factory's policy.
	FactoryID core.FactoryID
	Clock     core.Clock
}

// Baselines keeps the one policy revision in force and is the only way to
// change it.
type Baselines struct {
	cfg BaselinesConfig
}

// NewBaselines builds the baseline record, refusing a missing part.
func NewBaselines(cfg BaselinesConfig) (*Baselines, error) {
	if cfg.State == nil || cfg.Source == nil || cfg.Approvals == nil || cfg.Audit == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "the policy baseline needs a state store, a source, an approval store and an audit log")
	}
	if err := cfg.FactoryID.Validate(); err != nil {
		return nil, err
	}
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	return &Baselines{cfg: cfg}, nil
}

// Current returns the approved baseline, or NOT_FOUND before the first one.
func (b *Baselines) Current(ctx context.Context) (Baseline, error) {
	baseline, _, err := b.load(ctx)
	return baseline, err
}

// load reads the baseline and the version it is stored at.
func (b *Baselines) load(ctx context.Context) (Baseline, int64, error) {
	var baseline Baseline
	var version int64
	err := b.cfg.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, baselineNamespace, baselineKey)
		if err != nil {
			return err
		}
		version = entry.Version
		if err := json.Unmarshal(entry.Value, &baseline); err != nil {
			return core.NewError(core.CodeTampered, "the stored policy baseline cannot be read").WithCause(err)
		}
		return nil
	})
	return baseline, version, err
}

// Digest returns the digest an approval of revision must name: the content
// of the foundation files at that commit. It fails with POLICY_HOLD when
// the files there do not form a usable policy, so a broken revision cannot
// even be put up for approval.
func (b *Baselines) Digest(ctx context.Context, revision core.CommitSHA) (core.Digest, error) {
	snapshot, err := read(ctx, b.cfg.Source, revision)
	if err != nil {
		return "", err
	}
	return snapshot.Digest, nil
}

// Approve makes revision the policy in force.
//
// Only a person holding the Owner role can do this; an agent or a service is
// PERMISSION_DENIED whatever roles it presents, so nothing that is judged by
// the policy can rewrite it. The files at revision must form a complete,
// consistent policy of this factory, otherwise POLICY_HOLD.
//
// The first baseline, set at install, needs nothing more. Replacing a
// baseline is a policy change: approvalID must name an approval of exactly
// the new content, given under the baseline being replaced. It is spent
// here, once. Without one the answer is APPROVAL_REQUIRED.
//
// Every attempt is audited. A change that cannot be audited is not made.
func (b *Baselines) Approve(ctx context.Context, actor core.Actor, revision core.CommitSHA, approvalID string) (Baseline, error) {
	if err := revision.Validate(); err != nil {
		return Baseline{}, err
	}
	try := attempt{actor: actor, revision: revision, approvalID: approvalID}
	if actor.ID == "" || actor.Kind != core.ActorHuman || !actor.HasRole(core.RoleOwner) {
		return Baseline{}, b.refuse(ctx, try, core.NewError(core.CodePermissionDenied,
			"only the person who owns the factory can approve a policy revision"))
	}
	snapshot, err := read(ctx, b.cfg.Source, revision)
	if err != nil {
		return Baseline{}, b.refuse(ctx, try, err)
	}
	try.digest = snapshot.Digest
	if snapshot.Manifest.FactoryID != b.cfg.FactoryID {
		return Baseline{}, b.refuse(ctx, try, hold(reasonInvalid, "%s at %s declares factory %q, not this factory",
			PathFactory, revision, string(snapshot.Manifest.FactoryID)))
	}

	current, version, err := b.load(ctx)
	replacing := err == nil
	switch {
	case err != nil && !core.HasCode(err, core.CodeNotFound):
		return Baseline{}, b.refuse(ctx, try, err)
	case replacing && current.Revision == revision:
		return current, nil
	case replacing && approvalID == "":
		return Baseline{}, b.refuse(ctx, try, core.NewError(core.CodeApprovalRequired,
			"replacing the policy in force needs an approval of the new revision",
			core.NextAction{Action: "approvals.list", Description: "find or file the approval request for this revision"}))
	}
	try.replaces = current.Revision
	if replacing {
		if _, err := b.cfg.Approvals.Consume(ctx, approvalID, snapshot.Digest, current.Revision); err != nil {
			return Baseline{}, b.refuse(ctx, try, err)
		}
	}

	// The record of the permission is written before the change, so the
	// change never exists without a trace.
	if err := b.audit(ctx, try, core.OutcomeAllowed, nil); err != nil {
		return Baseline{}, core.NewError(core.CodeOf(err), "the policy change could not be audited, so it was not made").WithCause(err)
	}
	next := Baseline{Revision: revision, Digest: snapshot.Digest, ApprovedBy: actor.ID,
		ApprovedAt: b.cfg.Clock.Now(), ApprovalID: approvalID, Previous: current.Revision}
	raw, err := json.Marshal(next)
	if err != nil {
		return Baseline{}, core.NewError(core.CodeInternal, "the policy baseline cannot be encoded").WithCause(err)
	}
	err = b.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		// Writing at the version that was read refuses to overwrite a
		// baseline someone else set in the meantime.
		_, err := tx.PutKV(ctx, state.KVEntry{Namespace: baselineNamespace, Key: baselineKey, Value: raw, Version: version})
		return err
	})
	if err != nil {
		// The refusal is what the caller needs; a failed audit of it adds nothing.
		_ = b.audit(ctx, try, core.OutcomeFailed, err)
		return Baseline{}, err
	}
	if err := b.audit(ctx, try, core.OutcomeExecuted, nil); err != nil {
		return Baseline{}, core.NewError(core.CodeOf(err), "the policy revision is in force, but recording that failed").WithCause(err)
	}
	return next, nil
}

// attempt is one call of Approve, as the audit log describes it.
type attempt struct {
	actor      core.Actor
	revision   core.CommitSHA
	approvalID string
	digest     core.Digest
	replaces   core.CommitSHA
}

// refuse audits a refused attempt and returns the refusal. The refusal
// stands whether or not it could be audited.
func (b *Baselines) refuse(ctx context.Context, try attempt, refusal error) error {
	_ = b.audit(ctx, try, core.OutcomeDenied, refusal)
	return refusal
}

// audit appends one record about an attempt.
func (b *Baselines) audit(ctx context.Context, try attempt, outcome core.AuditOutcome, cause error) error {
	details := map[string]string{"revision": string(try.revision)}
	if try.approvalID != "" {
		details["approval_id"] = try.approvalID
	}
	if cause != nil {
		details["code"] = string(core.CodeOf(cause))
	}
	_, err := b.cfg.Audit.Append(ctx, core.AuditEvent{
		At: b.cfg.Clock.Now(), FactoryID: b.cfg.FactoryID, ActorID: try.actor.ID, ActorKind: try.actor.Kind,
		Surface: try.actor.Surface, Action: ActionApproveRevision, Resource: "policy:" + string(try.revision),
		SubjectDigest: try.digest, PolicySHA: try.replaces, Outcome: outcome, Details: details,
	})
	return err
}
