package oauthas

import (
	"cmp"
	"crypto/rand"
	"math/big"
	"net"
	"net/http"
	"slices"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/report"
)

// Registration is open to anyone, so the dynamically registered clients
// are bounded per network source and expire.
const (
	// unusedClientTTL is how long a client nobody has consented to stays
	// registered; an agent asks for consent right after registering.
	unusedClientTTL = time.Hour
	// idleClientTTL is how long a client stays registered after a person
	// last consented to it. Each authorization asks for consent again, so a
	// client in use renews it.
	idleClientTTL = 30 * 24 * time.Hour
)

// clientUse is the bookkeeping of one dynamically registered client.
type clientUse struct {
	// source registered the client; seq orders registrations.
	source string
	seq    uint64
	// registered is when it was added, lastUsed when a person last
	// consented to it, zero until one does.
	registered time.Time
	lastUsed   time.Time
}

func (u *clientUse) unused() bool { return u.lastUsed.IsZero() }

func (u *clientUse) expired(now time.Time) bool {
	if u.unused() {
		return !now.Before(u.registered.Add(unusedClientTTL))
	}
	return !now.Before(u.lastUsed.Add(idleClientTTL))
}

// clientKey names the network source of r: Config.ClientKey, or else the
// remote IP address.
func (s *Server) clientKey(r *http.Request) string {
	if s.cfg.ClientKey != nil {
		return s.cfg.ClientKey(r)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// admitRegistration takes a registration from source off its source's
// start limit (report.StartLimiter.Admit): StartsPerSource a minute. It
// reports whether the registration may go on and, if not, how long until
// it may be tried again.
func (s *Server) admitRegistration(source string) (bool, time.Duration) {
	s.mu.Lock()
	limits := s.registrationLimitsLocked()
	s.mu.Unlock()
	return limits.Admit(source, s.cfg.Clock.Now())
}

// registrationLimitsLocked returns the start limits of registrations, made
// on first use.
func (s *Server) registrationLimitsLocked() *report.StartLimiter {
	switch {
	case s.registrations != nil:
	case s.cfg.registrationLimits != nil:
		s.registrations = s.cfg.registrationLimits
	default:
		s.registrations = report.NewStartLimiter(s.cfg.MaxClients)
	}
	return s.registrations
}

// addDynamicClient stores c, registered from source, and reports whether
// it found room and, when it did not because the eviction rate is spent,
// how long until it may be tried again (retryAfter, zero otherwise). It
// first drops expired clients and then, while MaxClients are registered,
// counting the newcomer's source as if its new client were registered
// already, so a source that holds an unused client loses its own oldest
// before any other source's is touched:
//
//   - when some source holds more than one unused client, the oldest unused
//     client of the source holding the most: a flood from another address,
//     or from a few, does not reach a person's client, and one sharing the
//     person's address takes as many registrations as there are slots;
//   - when every source holds one, an unused client chosen at random
//     (crypto/rand), once the start limits allow another random eviction
//     (report.StartLimiter.Evict): a flood holding the table one client
//     from each of about MaxClients addresses cannot aim at a person's
//     client, and is held to the rate at which one consented to within a
//     minute survives at least 85% of the time;
//   - when no unused client is left, a client a person consented to,
//     chosen at random and charged the same way: one is never dropped
//     while an unused one remains.
//
// Only a random eviction is charged against that rate, so sources holding
// several unused clients each, however busy, never hold up a newcomer. A
// client a consent or code is pending for is never dropped; c is refused
// when the eviction rate is spent, or when every registered client is one
// of those. Only a person who holds a scope reaches consent, so an account
// without a role cannot pin a client this way.
func (s *Server) addDynamicClient(c Client, source string) (added bool, retryAfter time.Duration) {
	now := s.cfg.Clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	inFlight := s.clientsInFlightLocked(now)
	unusedBySource := map[string]int{source: 1}
	for id, u := range s.dynamic {
		switch {
		case u.expired(now) && !inFlight[id]:
			s.dropClientLocked(id)
		case u.unused() && !inFlight[id]:
			unusedBySource[u.source]++
		}
	}
	for len(s.dynamic) >= s.cfg.MaxClients {
		busiest := 0
		for _, n := range unusedBySource {
			busiest = max(busiest, n)
		}
		if busiest > 1 {
			s.dropOldestClientLocked(unusedBySource, func(id string, u *clientUse) bool {
				return u.unused() && !inFlight[id] && unusedBySource[u.source] == busiest
			})
			continue
		}
		if ok, wait := s.registrationLimitsLocked().Evict(now); !ok {
			return false, wait
		}
		// Only the newcomer's own source may be counted at one with no
		// unused client registered; then a consented one goes.
		if !s.dropRandomClientLocked(unusedBySource, func(id string, u *clientUse) bool { return u.unused() && !inFlight[id] }) &&
			!s.dropRandomClientLocked(unusedBySource, func(id string, _ *clientUse) bool { return !inFlight[id] }) {
			return false, 0
		}
	}
	s.clientSeq++
	s.clients[c.ID] = c
	s.dynamic[c.ID] = &clientUse{source: source, seq: s.clientSeq, registered: now}
	return true, 0
}

// dropRandomClientLocked drops a registration among those eligible picks,
// chosen at random, keeping unusedBySource current, and reports whether
// one was dropped; without randomness it drops none.
func (s *Server) dropRandomClientLocked(unusedBySource map[string]int, eligible func(string, *clientUse) bool) bool {
	type candidate struct {
		id string
		u  *clientUse
	}
	var candidates []candidate
	for id, u := range s.dynamic {
		if eligible(id, u) {
			candidates = append(candidates, candidate{id, u})
		}
	}
	if len(candidates) == 0 {
		return false
	}
	// Map order is neither uniform nor repeatable: the candidates are put
	// in the order they registered, so the choice is pick's alone.
	slices.SortFunc(candidates, func(a, b candidate) int { return cmp.Compare(a.u.seq, b.u.seq) })
	pick := s.pick
	if pick == nil {
		pick = randomIndex
	}
	i, err := pick(len(candidates))
	if err != nil || i < 0 || i >= len(candidates) {
		return false
	}
	victim := candidates[i]
	s.dropClientLocked(victim.id)
	if victim.u.unused() {
		unusedBySource[victim.u.source]--
	}
	return true
}

// randomIndex returns a uniformly random index below n from crypto/rand.
func randomIndex(n int) (int, error) {
	i, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, err
	}
	return int(i.Int64()), nil
}

// clientsInFlightLocked returns the clients a consent or an unredeemed code
// is pending for.
func (s *Server) clientsInFlightLocked(now time.Time) map[string]bool {
	ids := map[string]bool{}
	for _, p := range s.pending {
		if now.Before(p.expires) {
			ids[p.req.client.ID] = true
		}
	}
	for _, g := range s.codes {
		if !g.used && now.Before(g.expires) {
			ids[g.clientID] = true
		}
	}
	return ids
}

// dropOldestClientLocked drops the oldest registration among those
// eligible picks, keeping unusedBySource current.
func (s *Server) dropOldestClientLocked(unusedBySource map[string]int, eligible func(string, *clientUse) bool) {
	var victim string
	var oldest *clientUse
	for id, u := range s.dynamic {
		if eligible(id, u) && (oldest == nil || u.seq < oldest.seq) {
			victim, oldest = id, u
		}
	}
	if oldest == nil {
		return
	}
	s.dropClientLocked(victim)
	unusedBySource[oldest.source]--
}

func (s *Server) dropClientLocked(id string) {
	delete(s.clients, id)
	delete(s.dynamic, id)
}

// markClientUsedLocked records that a person consented to client id at now.
func (s *Server) markClientUsedLocked(id string, now time.Time) {
	if u, ok := s.dynamic[id]; ok {
		u.lastUsed = now
	}
}
