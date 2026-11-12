package egress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// ActionChangePolicy is the action of an approval request that changes the
// egress policy. An approval given for any other action never does.
const ActionChangePolicy = "egress.policy.change"

// Where the egress policy and its plans live. The namespace belongs to the
// control zone: no other zone may read or write it.
const (
	namespace         = "egress"
	policyKey         = "policy"
	planPrefix        = "plan/"
	defaultRequestTTL = 7 * 24 * time.Hour
	maxPlanRetries    = 20
)

// Baseline returns the egress policy of the policy revision the Owner
// approved, and that revision.
type Baseline interface {
	EgressBaseline(ctx context.Context) (core.EgressPolicy, core.CommitSHA, error)
}

// BaselineFunc adapts a function to Baseline.
type BaselineFunc func(ctx context.Context) (core.EgressPolicy, core.CommitSHA, error)

// EgressBaseline calls f.
func (f BaselineFunc) EgressBaseline(ctx context.Context) (core.EgressPolicy, core.CommitSHA, error) {
	return f(ctx)
}

// LoaderBaseline reads the egress section of the factory manifest at the
// approved policy revision.
func LoaderBaseline(l *policy.Loader) BaselineFunc {
	return func(ctx context.Context) (core.EgressPolicy, core.CommitSHA, error) {
		snapshot, err := l.Current(ctx)
		if err != nil {
			return core.EgressPolicy{}, "", err
		}
		return snapshot.Manifest.Egress, snapshot.Revision, nil
	}
}

// ApprovalStore is the part of the approval store the service uses;
// *approvals.KVStore is one.
type ApprovalStore interface {
	Create(ctx context.Context, r approvals.Request) (approvals.Request, error)
	Get(ctx context.Context, id string) (approvals.Request, error)
	ConsumeWith(ctx context.Context, requestID string, subject core.Digest, policy core.CommitSHA, write approvals.SpendFunc) (approvals.Approval, error)
}

// ServiceConfig is what a Service is built from. Every field but Clock and
// RequestTTL is required.
type ServiceConfig struct {
	FactoryID core.FactoryID
	// State is the control zone's store.
	State     state.Store
	Approvals ApprovalStore
	Audit     core.AuditLog
	Baseline  Baseline
	Clock     core.Clock
	// RequestTTL is how long a change waits for the Owner; seven days when zero.
	RequestTTL time.Duration
}

// Service governs the egress policy: plan, Owner approval, execute, audit.
// Every surface calls it; none changes the policy another way.
type Service struct{ cfg ServiceConfig }

// Effective is the policy in force and where it comes from.
type Effective struct {
	Policy core.EgressPolicy `json:"policy"`
	// Revision is the policy revision in force.
	Revision core.CommitSHA `json:"revision"`
	// RequestID is the approved change in force, "" when the manifest's
	// policy at Revision is.
	RequestID  string `json:"request_id,omitempty"`
	ApprovedBy string `json:"approved_by,omitempty"`
	// version is the stored document's version, for compare-and-swap.
	version int64
}

// Plan is a proposed change waiting for the Owner's decision.
type Plan struct {
	Request     approvals.Request `json:"request"`
	Current     core.EgressPolicy `json:"current"`
	Proposed    core.EgressPolicy `json:"proposed"`
	Change      Change            `json:"change"`
	RiskWarning string            `json:"risk_warning,omitempty"`
}

// storedPolicy is the approved change in force.
type storedPolicy struct {
	Policy       core.EgressPolicy `json:"policy"`
	BaseRevision core.CommitSHA    `json:"base_revision"`
	RequestID    string            `json:"request_id"`
	ApprovedBy   string            `json:"approved_by"`
	AppliedAt    time.Time         `json:"applied_at"`
}

// storedPlan is what an approval request was filed for.
type storedPlan struct {
	Proposed     core.EgressPolicy `json:"proposed"`
	Change       Change            `json:"change"`
	BaseRevision core.CommitSHA    `json:"base_revision"`
	BaseVersion  int64             `json:"base_version"`
	Digest       core.Digest       `json:"digest"`
	RequestedBy  string            `json:"requested_by"`
}

// NewService builds the service, refusing a configuration that lacks a part.
func NewService(cfg ServiceConfig) (*Service, error) {
	switch {
	case cfg.State == nil || cfg.Approvals == nil || cfg.Audit == nil || cfg.Baseline == nil:
		return nil, core.NewError(core.CodeInvalidArgument, "the egress policy service needs a store, approvals, an audit log and the approved baseline")
	case cfg.RequestTTL < 0:
		return nil, core.NewError(core.CodeInvalidArgument, "the request lifetime must not be negative")
	}
	if err := cfg.FactoryID.Validate(); err != nil {
		return nil, err
	}
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	if cfg.RequestTTL == 0 {
		cfg.RequestTTL = defaultRequestTTL
	}
	return &Service{cfg: cfg}, nil
}

// Current returns the policy in force: the last approved change while the
// policy revision it was approved under is in force, otherwise the
// manifest's policy at the approved revision. A newer approved revision
// thus replaces an older approved change.
func (s *Service) Current(ctx context.Context) (Effective, error) {
	base, revision, err := s.cfg.Baseline.EgressBaseline(ctx)
	if err != nil {
		return Effective{}, core.NewError(core.CodeOf(err), "the approved egress policy cannot be read").WithCause(err)
	}
	var doc storedPolicy
	var version int64
	err = s.cfg.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		version, err = loadDoc(ctx, tx, policyKey, &doc)
		return err
	})
	if err != nil {
		return Effective{}, err
	}
	if version > 0 && doc.BaseRevision == revision {
		return Effective{Policy: doc.Policy, Revision: revision, RequestID: doc.RequestID, ApprovedBy: doc.ApprovedBy, version: version}, nil
	}
	return Effective{Policy: Normalize(base), Revision: revision, version: version}, nil
}

// EgressPolicy returns the policy in force, so the service is the proxy's
// PolicySource.
func (s *Service) EgressPolicy(ctx context.Context) (core.EgressPolicy, error) {
	e, err := s.Current(ctx)
	return e.Policy, err
}

// loadDoc reads a document of the namespace and returns its version, 0 when
// it does not exist.
func loadDoc(ctx context.Context, tx state.Tx, key string, into any) (int64, error) {
	entry, err := tx.GetKV(ctx, namespace, key)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return 0, nil
	case err != nil:
		return 0, err
	}
	if err := json.Unmarshal(entry.Value, into); err != nil {
		return 0, core.NewError(core.CodeTampered, "a stored egress document cannot be read").WithCause(err)
	}
	return entry.Version, nil
}

// putDoc writes a document at the version read; 0 creates it.
func putDoc(ctx context.Context, tx state.Tx, key string, version int64, doc any) error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return core.NewError(core.CodeInternal, "an egress document cannot be encoded").WithCause(err)
	}
	_, err = tx.PutKV(ctx, state.KVEntry{Namespace: namespace, Key: key, Value: raw, Version: version})
	return err
}

// subjectDigest binds an approval to the exact proposed policy and to the
// policy it replaces.
func subjectDigest(revision core.CommitSHA, version int64, proposed core.EgressPolicy) (core.Digest, error) {
	raw, err := json.Marshal(struct {
		Revision core.CommitSHA    `json:"revision"`
		Version  int64             `json:"version"`
		Proposed core.EgressPolicy `json:"proposed"`
	}{revision, version, Normalize(proposed)})
	if err != nil {
		return "", core.NewError(core.CodeInternal, "the proposed policy cannot be encoded").WithCause(err)
	}
	sum := sha256.Sum256(raw)
	return core.Digest("sha256:" + hex.EncodeToString(sum[:])), nil
}

// isAgent reports whether the actor is an agent, by kind or by role.
func isAgent(a core.Actor) bool { return a.Kind == core.ActorAgent || a.HasRole(core.RoleAgent) }

// mayChange checks who may plan or execute a change: an Owner or a
// Maintainer, or an agent that only narrows the policy. The Owner's
// approval is required in every case.
func mayChange(a core.Actor, c Change) error {
	switch {
	case a.ID == "" || !a.Kind.Valid():
		return core.NewError(core.CodeUnauthenticated, "the actor could not be identified")
	case isAgent(a) && c.Widens():
		return core.NewError(core.CodePermissionDenied, "an agent cannot widen the egress policy or turn blocking off")
	case isAgent(a):
		return nil
	case !a.HasRole(core.RoleOwner) && !a.HasRole(core.RoleMaintainer):
		return core.NewError(core.CodePermissionDenied, "only the Owner or a Maintainer may propose an egress policy change")
	}
	return nil
}

func planID(digest core.Digest, n int) string {
	id := "egress:" + string(digest)[len("sha256:"):][:16]
	if n > 1 {
		id = fmt.Sprintf("%s:%d", id, n)
	}
	return id
}
