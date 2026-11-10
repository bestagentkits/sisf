package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"time"

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

// beginFlow starts a sign-in, or a step-up of the session under stepUpKey
// for target, and returns the provider URL and the flow cookie value.
func (s *Service) beginFlow(providerName, returnTo, stepUpKey string, target StepUpTarget) (authURL, binding string, err error) {
	p, ok := s.providers[providerName]
	if !ok {
		return "", "", core.NewError(core.CodeNotFound, "no login provider is named "+providerName)
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
	if stepUpKey != "" && canForce {
		authURL, err = fresh.StepUpURL(req)
	} else {
		authURL, err = p.AuthorizeURL(req)
	}
	if err != nil {
		return "", "", err
	}
	now := s.cfg.Clock.Now()
	f := &flow{provider: providerName, nonce: nonce, verifier: verifier, binding: digest(binding),
		returnTo: returnTo, started: now, expires: now.Add(flowTTL), stepUpKey: stepUpKey, stepUpFor: target}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneFlowsLocked(now)
	s.flows[digest(state)] = f
	return authURL, binding, nil
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
	if fresh, canForce := p.(FreshSignInProvider); f.stepUpKey != "" && canForce {
		id, err = fresh.ExchangeFresh(ctx, ex, f.started)
	} else {
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

// pruneFlowsLocked drops expired flows and, at the cap, the oldest one.
func (s *Service) pruneFlowsLocked(now time.Time) {
	for k, f := range s.flows {
		if !now.Before(f.expires) {
			delete(s.flows, k)
		}
	}
	for len(s.flows) >= maxFlows {
		var oldestKey string
		var oldest *flow
		for k, f := range s.flows {
			if oldest == nil || f.started.Before(oldest.started) {
				oldestKey, oldest = k, f
			}
		}
		delete(s.flows, oldestKey)
	}
}
