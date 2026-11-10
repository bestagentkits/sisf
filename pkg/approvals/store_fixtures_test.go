package approvals_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// fixture is one approval store over in-memory state, with the clock, the
// signing key and the audit log a test can drive and inspect.
type fixture struct {
	store *approvals.KVStore
	state *intaketest.State
	clock *intaketest.Clock
	keys  *intaketest.Keys
	audit *intaketest.AuditLog
}

// newFixture builds a store whose clock stands one minute after the sample
// request was created, the instant the sample decision is made.
func newFixture(t *testing.T) fixture {
	t.Helper()
	clock := intaketest.NewClock(created.Add(time.Minute))
	f := fixture{
		state: intaketest.NewState(clock),
		clock: clock,
		keys:  intaketest.NewKeys("approval-key-1"),
		audit: &intaketest.AuditLog{},
	}
	f.store = f.open(t)
	return f
}

// open builds another store over the same state, as a restart would.
func (f fixture) open(t *testing.T) *approvals.KVStore {
	t.Helper()
	store, err := approvals.NewStore(approvals.Config{
		State: f.state, Signer: f.keys, Verifier: f.keys, Audit: f.audit, Clock: f.clock,
	})
	if err != nil {
		t.Fatalf("store could not be built: %v", err)
	}
	return store
}

// pending stores the sample request after mutate has changed it.
func (f fixture) pending(t *testing.T, mutate func(*approvals.Request)) approvals.Request {
	t.Helper()
	r := validRequest()
	if mutate != nil {
		mutate(&r)
	}
	stored, err := f.store.Create(context.Background(), r)
	if err != nil {
		t.Fatalf("request could not be stored: %v", err)
	}
	return stored
}

// decided stores the sample request and records decision on it by a person
// who has just confirmed their identity.
func (f fixture) decided(t *testing.T, decision approvals.Decision) approvals.Request {
	t.Helper()
	f.pending(t, nil)
	a := validApproval()
	a.Decision = decision
	r, err := f.store.Decide(asOwner(a), a)
	if err != nil {
		t.Fatalf("decision could not be recorded: %v", err)
	}
	return r
}

// asOwner is the context of a call made for the Owner whom decision a names:
// the session layer authenticated that person and vouches for the identity
// fields of the decision.
func asOwner(a approvals.Approval) context.Context {
	return approvals.WithApprover(context.Background(), core.Actor{ID: a.ActorID, Kind: a.ActorKind,
		Roles: []core.Role{core.RoleOwner}, Surface: a.Surface, StepUpAt: a.StepUpAt})
}

// wantCode fails the test unless err carries code.
func wantCode(t *testing.T, err error, code core.ErrorCode, what string) {
	t.Helper()
	if !core.HasCode(err, code) {
		t.Fatalf("%s: want %s, got %v", what, code, err)
	}
}

// status reads the current status of the sample request.
func (f fixture) status(t *testing.T) approvals.Status {
	t.Helper()
	r, err := f.store.Get(context.Background(), "apr-0001")
	if err != nil {
		t.Fatalf("request could not be read: %v", err)
	}
	return r.Status
}
