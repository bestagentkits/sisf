package oauthas

import (
	"net"
	"net/http"
	"time"
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

// addDynamicClient stores c, registered from source. It first drops expired
// clients and then, while MaxClients are registered, the oldest unused
// client of the source holding the most, as long as that source holds more
// than one: a flood from another address, or from a few, does not reach a
// person's client, and one sharing the person's address takes as many
// registrations as there are slots. When no source holds more than one, a
// newcomer never drops an older client of another source: c is refused
// until unused clients lapse, so a flood of one registration from each of
// many addresses cannot drop a person's. A client a person consented to, or
// one a consent or code is pending for, is never dropped to make room. Only
// a person who holds a scope reaches consent, so an account without a role
// cannot pin a client this way.
func (s *Server) addDynamicClient(c Client, source string) bool {
	now := s.cfg.Clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	inFlight := s.clientsInFlightLocked(now)
	unusedBySource := map[string]int{}
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
		if busiest <= 1 {
			return false
		}
		s.dropOldestClientLocked(unusedBySource, func(id string, u *clientUse) bool {
			return u.unused() && !inFlight[id] && unusedBySource[u.source] == busiest
		})
	}
	s.clientSeq++
	s.clients[c.ID] = c
	s.dynamic[c.ID] = &clientUse{source: source, seq: s.clientSeq, registered: now}
	return true
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
