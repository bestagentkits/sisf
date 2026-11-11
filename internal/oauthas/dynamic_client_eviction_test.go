package oauthas

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// stillClock stands still until a test moves it.
type stillClock struct{ now time.Time }

func (c *stillClock) Now() time.Time                         { return c.now }
func (c *stillClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

var _ core.Clock = (*stillClock)(nil)

// clientTable is a server holding dynamically registered clients only, at
// most maxClients, choosing which to drop with rng.
func clientTable(clock core.Clock, maxClients int, rng *rand.Rand) *Server {
	s := &Server{cfg: Config{Clock: clock, MaxClients: maxClients}, clients: map[string]Client{},
		dynamic: map[string]*clientUse{}, pending: map[string]*pendingConsent{}, codes: map[string]*codeGrant{}}
	s.pick = func(n int) (int, error) { return rng.IntN(n), nil }
	return s
}

// register adds a client from source and returns its ID, or "" when it was
// refused.
func (s *Server) register(source string) string {
	s.mu.Lock()
	id := fmt.Sprintf("client-%d", s.clientSeq+1)
	s.mu.Unlock()
	if !s.addDynamicClient(Client{ID: id, Dynamic: true}, source) {
		return ""
	}
	return id
}

// A flood of one registration from each of many addresses, at about 1.7 a
// second, keeps every slot taken by sources holding one each. Every
// registration is still admitted, a client a person consented to is never
// dropped while unused ones remain, and a person's client consented to
// within a minute of its registration survives in at least 85% of cases:
// people register every other client of the flood, and each is checked a
// minute after it was registered. The test ends before the unused clients
// lapse, so every registration drops one, the worst case of that rate.
func TestAOnePerSourceFloodRarelyDropsAPersonsClient(t *testing.T) {
	t.Parallel()
	const people = 400
	interval := time.Second * 10 / 17
	perMinute := int(time.Minute / interval)
	clock := &stillClock{now: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)}
	begin := clock.now
	s := clientTable(clock, DefaultMaxClients, rand.New(rand.NewPCG(9, 10)))
	consented := s.register("owner")
	s.markClientUsedLocked(consented, clock.now)
	for n := 0; len(s.dynamic) < DefaultMaxClients; n++ {
		s.register(fmt.Sprintf("fill-%d", n))
	}
	type person struct {
		id         string
		registered int
	}
	var waiting []person
	survived, checked := 0, 0
	for n := 0; checked < people; n++ {
		clock.now = clock.now.Add(interval)
		source := fmt.Sprintf("flood-%d", n)
		if n%2 == 0 {
			source = fmt.Sprintf("person-%d", n)
		}
		id := s.register(source)
		if id == "" {
			t.Fatalf("registration %d was refused with every slot held by one unused client per source", n)
		}
		if n%2 == 0 {
			waiting = append(waiting, person{id: id, registered: n})
		}
		for len(waiting) > 0 && n-waiting[0].registered >= perMinute {
			if _, ok := s.dynamic[waiting[0].id]; ok {
				survived++
			}
			checked++
			waiting = waiting[1:]
		}
		if _, ok := s.dynamic[consented]; !ok {
			t.Fatalf("registration %d dropped a client a person consented to", n)
		}
	}
	if elapsed := clock.now.Sub(begin); elapsed >= unusedClientTTL {
		t.Fatalf("the flood ran %s, past the unused clients' lifetime", elapsed)
	}
	t.Logf("%d of %d people's clients survived a minute of the flood", survived, people)
	if rate := float64(survived) / people; rate < 0.85 {
		t.Fatalf("%d of %d people's clients survived a minute of the flood (%.0f%%), want at least 85%%", survived, people, rate*100)
	}
}

// With every slot held by clients people consented to, a registration
// drops one of those chosen at random rather than being refused; a client
// a consent or a code is pending for is never dropped, and with only such
// clients left the registration is refused.
func TestConsentedClientsMakeRoomOnlyWhenNoUnusedOneIsLeft(t *testing.T) {
	t.Parallel()
	clock := &stillClock{now: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)}
	s := clientTable(clock, 4, rand.New(rand.NewPCG(11, 12)))
	var used []string
	for n := range 4 {
		id := s.register(fmt.Sprintf("person-%d", n))
		s.markClientUsedLocked(id, clock.now)
		used = append(used, id)
	}
	s.codes["pending"] = &codeGrant{clientID: used[0], expires: clock.now.Add(time.Minute)}
	if s.register("newcomer") == "" {
		t.Fatal("a registration was refused with every slot held by consented clients")
	}
	if _, ok := s.dynamic[used[0]]; !ok {
		t.Fatal("a client a code is pending for was dropped")
	}
	if kept := len(s.dynamic); kept != 4 {
		t.Fatalf("%d clients registered, want the bound of 4", kept)
	}
	for id := range s.dynamic {
		s.codes["pending-"+id] = &codeGrant{clientID: id, expires: clock.now.Add(time.Minute)}
	}
	if s.register("another") != "" {
		t.Fatal("a registration was admitted though every client has a code pending")
	}
}
