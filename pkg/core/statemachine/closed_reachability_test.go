package statemachine

import (
	"context"
	"errors"
	"math/rand"
	"testing"
	"testing/quick"

	"github.com/bestagentkits/sisf/pkg/core"
)

// reachable returns every state an issue can end up in, starting from no
// record at all and taking only the edges that accept lets through.
func reachable(accept func(Edge) bool) map[core.State]bool {
	seen := map[core.State]bool{}
	frontier := []core.State{""}
	for len(frontier) > 0 {
		from := frontier[0]
		frontier = frontier[1:]
		for _, e := range Edges() {
			if e.From != from || seen[e.To] || !accept(e) {
				continue
			}
			seen[e.To] = true
			frontier = append(frontier, e.To)
		}
	}
	return seen
}

func TestEveryStateIsReachableFromANewIssue(t *testing.T) {
	seen := reachable(func(Edge) bool { return true })
	for _, state := range core.States() {
		if !seen[state] {
			t.Errorf("state %s can never be reached", state)
		}
	}
}

func TestClosedIsEnteredOnlyFromVerified(t *testing.T) {
	into := 0
	for _, e := range Edges() {
		if e.To != core.StateClosed {
			continue
		}
		into++
		if e.From != core.StateVerified {
			t.Errorf("edge on %s closes an issue from %s", e.Event, e.From)
		}
	}
	if into != 1 {
		t.Fatalf("%d edges enter Closed, want exactly 1", into)
	}
	for _, from := range sourceStates() {
		for _, event := range core.Events() {
			next, err := Next(from, event)
			if err == nil && next == core.StateClosed && from != core.StateVerified {
				t.Errorf("%q on %s closes the issue without verification", from, event)
			}
		}
	}
}

func TestClosedCannotBeReachedOnceVerifiedIsTakenOutOfTheLifecycle(t *testing.T) {
	withoutVerified := reachable(func(e Edge) bool {
		return e.From != core.StateVerified && e.To != core.StateVerified
	})
	if withoutVerified[core.StateClosed] {
		t.Fatal("an issue reached Closed on a path that never visits Verified")
	}
	// Only Verified and Closed may be lost: the removal must not hide other states.
	if len(withoutVerified) != len(core.States())-2 {
		t.Fatalf("without Verified %d states are reachable, want 13", len(withoutVerified))
	}
}

func TestRandomEventSequencesNeverCloseAnIssueThatWasNotVerified(t *testing.T) {
	events := core.Events()
	closedWalks := 0
	walk := func(seed int64) bool {
		rng := rand.New(rand.NewSource(seed))
		state, verified := core.State(""), false
		for step := 0; step < 400; step++ {
			// Half the events are arbitrary, half are ones the current state
			// accepts, so walks both probe illegal moves and make progress.
			event := events[rng.Intn(len(events))]
			if out := eventsFrom(state); len(out) > 0 && rng.Intn(2) == 0 {
				event = out[rng.Intn(len(out))]
			}
			next, err := Next(state, event)
			if err != nil {
				if !core.HasCode(err, core.CodeInvalidTransition) {
					return false
				}
				continue
			}
			if next == core.StateClosed && (state != core.StateVerified || !verified) {
				return false
			}
			if state == core.StateClosed {
				return false // nothing may move a closed issue
			}
			verified = verified || next == core.StateVerified
			state = next
		}
		if state == core.StateClosed {
			closedWalks++
		}
		return true
	}
	config := &quick.Config{MaxCount: 1000, Rand: rand.New(rand.NewSource(20261110))}
	if err := quick.Check(walk, config); err != nil {
		t.Fatalf("an event sequence closed an unverified issue or moved a closed one: %v", err)
	}
	if closedWalks == 0 {
		t.Fatal("no random walk ever closed an issue, so the property was never exercised")
	}
}

// acceptedBy lets through the edges a machine with these guards accepts.
func acceptedBy(guards Guards) func(Edge) bool {
	machine := machineWith(guards)
	return func(e Edge) bool {
		return machine.Check(context.Background(), transitionOn(e.From, e.Event, e.To)) == nil
	}
}

func TestClosedIsUnreachableWhileAGuardOnTheWayRefuses(t *testing.T) {
	if !reachable(acceptedBy(passingGuards()))[core.StateClosed] {
		t.Fatal("with every guard satisfied an issue must be able to close")
	}

	refused := errors.New("no proof")
	cases := map[string]func(*Guards){
		"no claim proof": func(g *Guards) {
			g.Claim = ClaimGuardFunc(func(context.Context, core.Transition) (ClaimProof, error) { return ClaimProof{}, refused })
		},
		"no passing receipt": func(g *Guards) {
			g.QA = QAGuardFunc(func(context.Context, core.Transition) (QAProof, error) { return QAProof{}, refused })
		},
		"no post-deploy proof": func(g *Guards) {
			g.PostDeploy = PostDeployGuardFunc(func(context.Context, core.Transition) (PostDeployProof, error) {
				return PostDeployProof{}, refused
			})
		},
		"no way into Ready is proven": func(g *Guards) {
			g.Delegation = DelegationGuardFunc(func(context.Context, core.Transition) (DelegationProof, error) {
				return DelegationProof{}, refused
			})
			g.Approval = ApprovalGuardFunc(func(context.Context, core.Transition) (ApprovalProof, error) {
				return ApprovalProof{}, refused
			})
			g.Retry = RetryGuardFunc(func(context.Context, core.Transition) (core.AuthzDecision, error) {
				return core.AuthzDecision{}, refused
			})
		},
		"no guard into Ready is wired": func(g *Guards) { g.Delegation, g.Approval, g.Retry = nil, nil, nil },
		"claim guard not wired":        func(g *Guards) { g.Claim = nil },
		"qa guard not wired":           func(g *Guards) { g.QA = nil },
		"post-deploy guard not wired":  func(g *Guards) { g.PostDeploy = nil },
	}
	for name, withhold := range cases {
		guards := passingGuards()
		withhold(&guards)
		seen := reachable(acceptedBy(guards))
		if seen[core.StateVerified] || seen[core.StateClosed] {
			t.Errorf("%s: the issue still reached Verified or Closed", name)
		}
	}

	// One proven way into Ready is enough: a scope that needs the Owner is
	// not stuck because delegation was refused, and the other way round.
	for name, withhold := range map[string]func(*Guards){
		"delegation refused, approval given": func(g *Guards) { g.Delegation = nil },
		"approval missing, scope delegated":  func(g *Guards) { g.Approval = nil },
	} {
		guards := passingGuards()
		withhold(&guards)
		if !reachable(acceptedBy(guards))[core.StateClosed] {
			t.Errorf("%s: the issue can no longer close", name)
		}
	}
}

func TestWithoutScopeProofReadyIsReachedOnlyThroughAnAuthorizedRetry(t *testing.T) {
	guards := passingGuards()
	guards.Delegation, guards.Approval = nil, nil

	// A scope that is neither delegated nor approved can still be blocked and
	// then retried, so the retry guard is the one check left on that path.
	viaRetry := reachable(acceptedBy(guards))
	if !viaRetry[core.StateBlocked] || !viaRetry[core.StateReady] {
		t.Fatal("a denied approval must still be able to block the issue and an authorized retry to ready it")
	}
	withoutBlocked := reachable(func(e Edge) bool {
		return e.From != core.StateBlocked && e.To != core.StateBlocked && acceptedBy(guards)(e)
	})
	if withoutBlocked[core.StateReady] {
		t.Fatal("an issue became Ready with neither delegation, approval nor a retry")
	}

	guards.Retry = retryWith(core.AuthzDecision{Effect: core.EffectDeny})
	if reachable(acceptedBy(guards))[core.StateReady] {
		t.Fatal("an issue became Ready although every way into it was refused")
	}
}
