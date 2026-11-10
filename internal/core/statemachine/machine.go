package statemachine

import (
	"context"
	"errors"

	"github.com/bestagentkits/sisf/internal/core"
)

// Machine decides whether a requested transition may be written. It holds no
// state of its own and is safe for concurrent use as long as its guards are.
// Building one is cheap, so a caller whose proof lives inside a store
// transaction builds a machine for that transaction.
type Machine struct {
	clock  core.Clock
	guards Guards
}

// New builds a machine that reads time from clock and proofs from guards. A
// nil clock means the wall clock. Guards left nil refuse their edges.
func New(clock core.Clock, guards Guards) *Machine {
	if clock == nil {
		clock = core.SystemClock{}
	}
	return &Machine{clock: clock, guards: guards}
}

// Check reports whether t is a legal step whose guard holds. It returns nil
// only then, and changes nothing either way.
//
// The checks run in a fixed order and stop at the first failure:
// INVALID_TRANSITION when the table has no edge from t.From on t.Event or the
// edge does not lead to t.To; INVALID_ARGUMENT when the request is malformed;
// GUARD_FAILED when the edge is guarded and its proof is absent or does not
// hold. A guard is consulted only for a legal, well-formed request, and a
// typed error returned by a guard keeps its own code.
func (m *Machine) Check(ctx context.Context, t core.Transition) error {
	edge, ok := lookup(t.From, t.Event)
	if !ok || edge.To != t.To {
		return noEdge(t.From, t.Event, t.To)
	}
	if err := t.Validate(); err != nil {
		return err
	}
	return m.checkGuard(ctx, edge.Guard, t)
}

// checkGuard asks the guard of an edge for its proof and judges it. Each
// case fails closed: no guard wired, no answer, or an answer that falls short
// all refuse the step.
func (m *Machine) checkGuard(ctx context.Context, guard GuardKind, t core.Transition) error {
	switch guard {
	case GuardNone:
		return nil
	case GuardClaim:
		if m.guards.Claim == nil {
			return unwired(guard, t)
		}
		proof, err := m.guards.Claim.ClaimProof(ctx, t)
		if err != nil {
			return unproven(guard, t, err)
		}
		return checkClaim(t, proof, m.clock.Now())
	case GuardQAPass:
		if m.guards.QA == nil {
			return unwired(guard, t)
		}
		proof, err := m.guards.QA.QAProof(ctx, t)
		if err != nil {
			return unproven(guard, t, err)
		}
		return checkQAPass(t, proof, m.clock.Now())
	case GuardPostDeploy:
		if m.guards.PostDeploy == nil {
			return unwired(guard, t)
		}
		proof, err := m.guards.PostDeploy.PostDeployProof(ctx, t)
		if err != nil {
			return unproven(guard, t, err)
		}
		return checkPostDeploy(t, proof, m.clock.Now())
	case GuardRetry:
		if m.guards.Retry == nil {
			return unwired(guard, t)
		}
		decision, err := m.guards.Retry.AuthorizeRetry(ctx, t)
		if err != nil {
			return unproven(guard, t, err)
		}
		return checkRetry(t, decision)
	}
	// An edge with a guard this machine does not know must not pass.
	return refuse(guard, t, "the step is protected by an unknown guard %q", string(guard))
}

// unwired refuses a guarded step for which no guard was provided.
func unwired(guard GuardKind, t core.Transition) error {
	return refuse(guard, t, "no %s guard is available, so the step cannot be proven", string(guard))
}

// unproven turns a guard's failure to answer into a refusal. A typed error
// is the guard's own, more specific refusal and is returned as it is. Any
// other error becomes GUARD_FAILED: its text may describe internals, so it is
// kept as the cause for operators and left out of the message.
func unproven(guard GuardKind, t core.Transition, err error) error {
	var typed *core.Error
	if errors.As(err, &typed) {
		return err
	}
	return refuse(guard, t, "the %s guard could not establish its proof", string(guard)).WithCause(err)
}
