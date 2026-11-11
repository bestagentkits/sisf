package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"slices"
	"strings"
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
	// source is the network source that started the flow (Config.ClientKey)
	// and seq the order flows were started in.
	source string
	seq    uint64
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
	s.pruneFlowsLocked(now, source)
	s.flowSeq++
	f.seq = s.flowSeq
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

// goesBefore reports whether f is dropped before other when room is needed:
// a sign-in before a step-up, and the older of two of a kind.
func (f *flow) goesBefore(other *flow) bool {
	if signIn := f.stepUpKey == ""; signIn != (other.stepUpKey == "") {
		return signIn
	}
	return f.seq < other.seq
}

// pruneFlowsLocked makes room for a flow from source. Anyone may start a
// sign-in, so it drops expired flows, then the oldest flow of source while
// source holds maxFlowsPerSource, and then, while all maxFlows are taken, a
// sign-in of the source holding the most: a flood, from one address or
// many, pushes out its own flows before a person's single one. A step-up,
// which only a signed-in session can start, goes only when nothing but
// step-ups is left.
func (s *Service) pruneFlowsLocked(now time.Time, source string) {
	perSource := map[string]int{}
	for k, f := range s.flows {
		if !now.Before(f.expires) {
			delete(s.flows, k)
			continue
		}
		if f.stepUpKey == "" {
			perSource[f.source]++
		}
	}
	for perSource[source] >= maxFlowsPerSource {
		s.dropFlowLocked(perSource, func(f *flow) bool { return f.source == source })
	}
	for len(s.flows) >= maxFlows {
		busiest := 0
		for _, n := range perSource {
			busiest = max(busiest, n)
		}
		s.dropFlowLocked(perSource, func(f *flow) bool { return busiest == 0 || perSource[f.source] == busiest })
	}
}

// dropFlowLocked drops the first flow to go among those eligible picks: a
// sign-in before a step-up, the oldest first. It keeps perSource, the count
// of open sign-ins by source, current.
func (s *Service) dropFlowLocked(perSource map[string]int, eligible func(*flow) bool) {
	var key string
	var victim *flow
	for k, f := range s.flows {
		if !eligible(f) {
			continue
		}
		if victim == nil || f.goesBefore(victim) {
			key, victim = k, f
		}
	}
	if victim == nil {
		return
	}
	delete(s.flows, key)
	if victim.stepUpKey == "" {
		perSource[victim.source]--
	}
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
