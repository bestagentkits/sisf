package approvals

import (
	"context"
	"sort"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// DefaultStepUpMaxAge is how recently a person must have re-confirmed their
// identity for a request that demands step-up. It is a starting value.
const DefaultStepUpMaxAge = 5 * time.Minute

// Config is what an approval store is built from. Every field but Clock and
// StepUpMaxAge is required: a store that could not sign, verify or audit
// would hand out approvals nobody can check afterwards.
type Config struct {
	// State is the operational store of the factory. Requests and decisions
	// live in it, so single use survives a restart.
	State state.Store
	// Signer signs each recorded decision. It is the delegated key of the
	// approval service; no agent or candidate ever holds it.
	Signer core.Signer
	// Verifier checks that signature again before an approval is spent.
	Verifier core.Verifier
	// Audit receives one record for every request, decision and use, and
	// for every one that was refused.
	Audit core.AuditLog
	// Clock is the source of time; nil means the wall clock.
	Clock core.Clock
	// StepUpMaxAge overrides DefaultStepUpMaxAge when positive.
	StepUpMaxAge time.Duration
}

// KVStore is the Store kept in the operational store of the factory. Each
// operation is one transaction, so a decision is recorded once and an
// approval is spent once even when several callers race.
type KVStore struct {
	state    state.Store
	signer   core.Signer
	verifier core.Verifier
	audit    core.AuditLog
	clock    core.Clock
	stepUp   time.Duration
}

var _ Store = (*KVStore)(nil)

// NewStore builds the store, refusing a configuration that lacks a part.
func NewStore(cfg Config) (*KVStore, error) {
	switch {
	case cfg.State == nil || cfg.Signer == nil || cfg.Verifier == nil || cfg.Audit == nil:
		return nil, invalid("an approval store needs a state store, a signer, a verifier and an audit log")
	case cfg.StepUpMaxAge < 0:
		return nil, invalid("the step-up age must not be negative")
	}
	s := &KVStore{state: cfg.State, signer: cfg.Signer, verifier: cfg.Verifier, audit: cfg.Audit,
		clock: cfg.Clock, stepUp: cfg.StepUpMaxAge}
	if s.clock == nil {
		s.clock = core.SystemClock{}
	}
	if s.stepUp == 0 {
		s.stepUp = DefaultStepUpMaxAge
	}
	return s, nil
}

// Create stores a valid pending request. A repeated ID is ALREADY_EXISTS, and
// so is a nonce another request already carries, because a nonce that two
// requests share could move a decision from one to the other.
func (s *KVStore) Create(ctx context.Context, r Request) (Request, error) {
	if err := r.Validate(); err != nil {
		return Request{}, err
	}
	switch {
	case r.Status != StatusPending:
		return Request{}, invalid("a new approval request must be pending")
	case r.Expired(s.clock.Now()):
		return Request{}, core.NewError(core.CodeExpired, "the approval request has already lapsed")
	}
	err := s.update(ctx, func(ctx context.Context, tx state.Tx) error {
		if err := absent(ctx, tx, requestKey(r.ID), "an approval request with this id already exists"); err != nil {
			return err
		}
		if err := absent(ctx, tx, nonceKey(r.Nonce), "the nonce already belongs to another approval request"); err != nil {
			return err
		}
		if err := put(ctx, tx, nonceKey(r.Nonce), 0, r.ID); err != nil {
			return err
		}
		return put(ctx, tx, requestKey(r.ID), 0, r)
	})
	if err != nil {
		return Request{}, err
	}
	if err := s.record(ctx, r, actionRequest, s.service(r.RequestedBy), nil); err != nil {
		return Request{}, err
	}
	return r, nil
}

// Get returns the request, with a pending request that has lapsed shown as
// expired. An unknown ID is NOT_FOUND.
func (s *KVStore) Get(ctx context.Context, id string) (Request, error) {
	var r Request
	err := s.state.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		r, _, err = loadRequest(ctx, tx, id)
		return err
	})
	if err != nil {
		return Request{}, err
	}
	return s.current(r), nil
}

// List returns the requests the filter selects, oldest first.
func (s *KVStore) List(ctx context.Context, f Filter) ([]Request, error) {
	var out []Request
	err := s.state.View(ctx, func(ctx context.Context, tx state.Tx) error {
		entries, err := tx.ListKV(ctx, namespace, requestPrefix, listAll)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			var r Request
			if err := decode(entry, &r); err != nil {
				return err
			}
			r = s.current(r)
			if (len(f.Statuses) == 0 || containsStatus(f.Statuses, r.Status)) && (f.IssueID.IsZero() || f.IssueID == r.IssueID) {
				out = append(out, r)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// Decision returns the decision recorded for a request, or NOT_FOUND when
// the request is unknown or still undecided.
func (s *KVStore) Decision(ctx context.Context, requestID string) (Approval, error) {
	var a Approval
	err := s.state.View(ctx, func(ctx context.Context, tx state.Tx) error {
		if _, _, err := loadRequest(ctx, tx, requestID); err != nil {
			return err
		}
		var err error
		a, err = loadDecision(ctx, tx, requestID)
		return err
	})
	if err != nil {
		return Approval{}, err
	}
	return a, nil
}

// current shows a pending request that passed its expiry as expired. The
// stored status is left alone: reading never writes.
func (s *KVStore) current(r Request) Request {
	if r.Status == StatusPending && r.Expired(s.clock.Now()) {
		r.Status = StatusExpired
	}
	return r
}

// update runs fn in a write transaction and runs it again when another
// writer got there first, so the loser sees what the winner wrote. A writer
// that keeps losing reports the conflict.
func (s *KVStore) update(ctx context.Context, fn func(ctx context.Context, tx state.Tx) error) error {
	var err error
	for range writeAttempts {
		if err = s.state.Update(ctx, fn); !core.HasCode(err, core.CodeConflict) {
			return err
		}
	}
	return err
}

func containsStatus(statuses []Status, s Status) bool {
	for _, candidate := range statuses {
		if candidate == s {
			return true
		}
	}
	return false
}
