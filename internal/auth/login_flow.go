package auth

import (
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"slices"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/identity"
)

// flow is one sign-in in progress. It is stored under the digest of its
// state, used once, and bound to the browser that started it by the digest
// of a random value held in the flow cookie.
type flow struct {
	provider string
	nonce    string
	verifier string
	binding  string
	returnTo string
	started  time.Time
	expires  time.Time
	// source is the network source that started the flow (Config.ClientKey)
	// and seq the order flows were started in.
	source string
	seq    uint64
	// protected marks a step-up of a person who holds a role; it is the
	// last to be dropped to make room.
	protected bool
	// stepUpKey is the session being stepped up; empty for a sign-in.
	stepUpKey string
	// stepUpFor is what the step-up confirms.
	stepUpFor StepUpTarget
}

// flowResult is the outcome of a completed flow.
type flowResult struct {
	returnTo string
	// cookie and session are set for a sign-in; a step-up sets neither.
	cookie  string
	session *session
}

// beginFlow starts a sign-in from source, or a step-up of the session under
// stepUpKey for target, and returns the provider URL and the flow cookie
// value.
func (s *Service) beginFlow(providerName, returnTo, source, stepUpKey string, target StepUpTarget) (authURL, binding string, err error) {
	p, ok := s.providers[providerName]
	if !ok {
		return "", "", core.NewError(core.CodeNotFound, "no login provider is named "+providerName)
	}
	if err := s.admitStart(source, stepUpKey); err != nil {
		return "", "", err
	}
	var state, nonce, verifier string
	for _, v := range []*string{&state, &nonce, &verifier, &binding} {
		if *v, err = randomToken(32); err != nil {
			return "", "", err
		}
	}
	sum := sha256.Sum256([]byte(verifier))
	req := identity.AuthRequest{State: state, Nonce: nonce, CodeChallenge: base64.RawURLEncoding.EncodeToString(sum[:])}
	fresh, canForce := p.(FreshSignInProvider)
	switch {
	case stepUpKey != "" && !canForce:
		return "", "", s.stepUpUnsupported(providerName)
	case stepUpKey != "":
		authURL, err = fresh.StepUpURL(req)
	default:
		authURL, err = p.AuthorizeURL(req)
	}
	if err != nil {
		return "", "", err
	}
	now := s.cfg.Clock.Now()
	f := &flow{provider: providerName, nonce: nonce, verifier: verifier, binding: digest(binding),
		returnTo: returnTo, started: now, expires: now.Add(flowTTL), source: source, stepUpKey: stepUpKey, stepUpFor: target}
	s.mu.Lock()
	defer s.mu.Unlock()
	f.protected = s.protectedStepUpLocked(stepUpKey)
	if !s.pruneFlowsLocked(now, source, stepUpKey, f.protected) {
		return "", "", core.NewError(core.CodeRateLimited, "too many sign-ins are open; try again in a few minutes")
	}
	s.flowSeq++
	f.seq = s.flowSeq
	s.flows[digest(state)] = f
	return authURL, binding, nil
}

// admitStart takes a start from source off the start limits
// (report.NewStartLimiter): StartsPerSource a minute from one source and,
// from all together, the rate at which a flood rotating through new
// sources pushes out a person's sign-in finished within a minute with
// probability at most 15%. A start beyond them is refused with
// RATE_LIMITED and the seconds until it may be tried again. A step-up of a
// person who holds a role is exempt: it is bounded per session
// (maxStepUpsPerSession), needs that person's session, and must start
// while a flood holds the limits.
func (s *Service) admitStart(source, stepUpKey string) error {
	s.mu.Lock()
	if s.starts == nil {
		s.starts = report.NewStartLimiter(maxFlows)
	}
	protected, starts := s.protectedStepUpLocked(stepUpKey), s.starts
	s.mu.Unlock()
	if protected {
		return nil
	}
	ok, wait := starts.Take(source, s.cfg.Clock.Now())
	if ok {
		return nil
	}
	err := core.NewError(core.CodeRateLimited, "too many sign-ins are being started; try again shortly")
	err.Details = map[string]string{"retry_after_seconds": report.RetryAfter(wait)}
	return err
}

// protectedStepUpLocked reports whether a step-up of the session under
// stepUpKey is protected: its person holds a role (Config.HoldsRole).
func (s *Service) protectedStepUpLocked(stepUpKey string) bool {
	ss, ok := s.sessions[stepUpKey]
	return ok && stepUpKey != "" && s.cfg.HoldsRole != nil && s.cfg.HoldsRole(ss.actorID)
}

// completeFlow finishes the flow named by state. The flow is consumed before
// any check, so a state is never accepted twice. For a step-up,
// sessionKey must be the session the step-up was started from.
func (s *Service) completeFlow(ctx context.Context, providerName, state, code, binding, sessionKey string) (flowResult, error) {
	s.mu.Lock()
	f, ok := s.flows[digest(state)]
	delete(s.flows, digest(state))
	s.mu.Unlock()
	now := s.cfg.Clock.Now()
	switch {
	case state == "" || !ok:
		return flowResult{}, unauthenticated("unknown or already used sign-in state")
	case !now.Before(f.expires):
		return flowResult{}, unauthenticated("the sign-in took too long; start again")
	case f.provider != providerName:
		return flowResult{}, unauthenticated("the sign-in was started with another provider")
	case subtle.ConstantTimeCompare([]byte(digest(binding)), []byte(f.binding)) != 1:
		return flowResult{}, unauthenticated("the sign-in was started in another browser")
	case f.stepUpKey != "" && subtle.ConstantTimeCompare([]byte(f.stepUpKey), []byte(sessionKey)) != 1:
		return flowResult{}, unauthenticated("step-up must finish in the session that started it")
	}
	p := s.providers[providerName]
	ex := identity.Exchange{Code: code, Nonce: f.nonce, CodeVerifier: f.verifier}
	var id identity.Identity
	var err error
	fresh, canForce := p.(FreshSignInProvider)
	switch {
	case f.stepUpKey != "" && !canForce:
		return flowResult{}, s.stepUpUnsupported(providerName)
	case f.stepUpKey != "":
		id, err = fresh.ExchangeFresh(ctx, ex, f.started)
	default:
		id, err = p.Exchange(ctx, ex)
	}
	if err != nil {
		if core.CodeOf(err) == core.CodeInternal {
			return flowResult{}, unauthenticated("the sign-in could not be validated").WithCause(err)
		}
		return flowResult{}, err
	}
	if err := id.Validate(); err != nil || id.Issuer != p.Issuer() {
		return flowResult{}, unauthenticated("the provider returned an identity from another issuer")
	}
	if f.stepUpKey != "" {
		return flowResult{returnTo: f.returnTo}, s.markStepUp(f.stepUpKey, id, f.stepUpFor)
	}
	cookie, ss, err := s.createSession(id)
	if err != nil {
		return flowResult{}, err
	}
	return flowResult{returnTo: f.returnTo, cookie: cookie, session: ss}, nil
}

// pruneFlowsLocked makes room for a flow from source, a step-up of the
// session under stepUpKey when that is set, and reports whether there is
// room. Anyone may start a sign-in and any account can sign in and start
// step-ups, so it drops expired flows, then the session's oldest step-up
// while it holds maxStepUpsPerSession, and then, while all maxFlows are
// taken:
//
//   - when some source holds more than one unprotected flow, the oldest
//     flow of the source holding the most: a flood from one address, or from
//     a few, pushes out its own flows before a person's single one, and one
//     sharing the person's address takes as many flows to reach theirs as
//     there are slots;
//   - when every source holds one, an unprotected flow chosen at random
//     (crypto/rand): a new sign-in is never refused because the table is
//     full of one flow from each of many addresses, so such a flood cannot
//     lock everyone out, and it cannot aim at a person's flow either: at
//     1.7 starts a second against 1,024 slots, a sign-in finished within a
//     minute survives about nine times in ten.
//
// A step-up of a person who holds a role (Config.HoldsRole) is protected:
// it is never counted against a source and never pushed out for an
// unprotected flow. It always finds room: when nothing else is left, it
// pushes out the oldest protected flow. An unprotected flow is refused only
// when every flow left is protected.
func (s *Service) pruneFlowsLocked(now time.Time, source, stepUpKey string, protected bool) bool {
	// The newcomer's source is counted as if its new flow were in the
	// table already, so a source that holds a flow loses its own oldest
	// before any other source's flow is touched.
	perSource := map[string]int{}
	if !protected {
		perSource[source]++
	}
	stepUps := 0
	for k, f := range s.flows {
		switch {
		case !now.Before(f.expires):
			delete(s.flows, k)
			continue
		case !f.protected:
			perSource[f.source]++
		}
		if stepUpKey != "" && f.stepUpKey == stepUpKey {
			stepUps++
		}
	}
	for ; stepUps >= maxStepUpsPerSession; stepUps-- {
		s.dropOldestFlowLocked(perSource, func(f *flow) bool { return f.stepUpKey == stepUpKey })
	}
	for len(s.flows) >= maxFlows {
		busiest := 0
		for _, n := range perSource {
			busiest = max(busiest, n)
		}
		var dropped bool
		switch {
		case busiest > 1:
			dropped = s.dropOldestFlowLocked(perSource, func(f *flow) bool { return !f.protected && perSource[f.source] == busiest })
		case busiest == 1:
			dropped = s.dropRandomFlowLocked(perSource)
		case protected:
			dropped = s.dropOldestFlowLocked(perSource, func(*flow) bool { return true })
		}
		if !dropped {
			return false
		}
	}
	return true
}

// dropRandomFlowLocked drops an unprotected flow chosen at random, keeping
// perSource current, and reports whether one was dropped; without
// randomness it drops none.
func (s *Service) dropRandomFlowLocked(perSource map[string]int) bool {
	type candidate struct {
		key string
		f   *flow
	}
	candidates := make([]candidate, 0, len(s.flows))
	for k, f := range s.flows {
		if !f.protected {
			candidates = append(candidates, candidate{k, f})
		}
	}
	if len(candidates) == 0 {
		return false
	}
	// Map order is neither uniform nor repeatable: the candidates are put
	// in the order they started, so the choice is pick's alone.
	slices.SortFunc(candidates, func(a, b candidate) int { return cmp.Compare(a.f.seq, b.f.seq) })
	pick := s.pick
	if pick == nil {
		pick = randomIndex
	}
	i, err := pick(len(candidates))
	if err != nil || i < 0 || i >= len(candidates) {
		return false
	}
	victim := candidates[i]
	delete(s.flows, victim.key)
	perSource[victim.f.source]--
	return true
}

// dropOldestFlowLocked drops the oldest flow among those eligible picks,
// keeping perSource, the count of unprotected flows by source, current. It
// reports whether one was dropped.
func (s *Service) dropOldestFlowLocked(perSource map[string]int, eligible func(*flow) bool) bool {
	var key string
	var victim *flow
	for k, f := range s.flows {
		if eligible(f) && (victim == nil || f.seq < victim.seq) {
			key, victim = k, f
		}
	}
	if victim == nil {
		return false
	}
	delete(s.flows, key)
	if !victim.protected {
		perSource[victim.source]--
	}
	return true
}

// stepUpUnsupported refuses a step-up through a provider that cannot force
// a fresh sign-in, such as GitHub OAuth: it would replay the provider's own
// session rather than confirm the person, so it names the providers that
// can. Signing in through it is unaffected.
func (s *Service) stepUpUnsupported(providerName string) error {
	able := s.stepUpProviders("")
	msg := providerName + " cannot force a fresh sign-in, so it cannot confirm your identity"
	if len(able) == 0 {
		return core.NewError(core.CodeNotSupported, msg+"; configure an OpenID Connect provider to confirm through")
	}
	return core.NewError(core.CodeNotSupported, msg+"; confirm through an OpenID Connect provider instead: "+strings.Join(able, ", "))
}

// stepUpProviders returns, sorted, the names of the providers that can force
// a fresh sign-in and, when issuer is set, sign in identities of that
// issuer: the ones a session of issuer can confirm itself through.
func (s *Service) stepUpProviders(issuer string) []string {
	able := []string{}
	for name, p := range s.providers {
		if _, ok := p.(FreshSignInProvider); ok && (issuer == "" || p.Issuer() == issuer) {
			able = append(able, name)
		}
	}
	slices.Sort(able)
	return able
}
