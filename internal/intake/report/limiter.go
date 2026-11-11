package report

import (
	"math"
	"strconv"
	"sync"
	"time"
)

// Limiter counts requests in fixed windows, per client and overall. The
// client table is bounded: when it is full and nothing in it has expired, a
// new client is refused, so many addresses cannot exhaust memory. It is
// safe for concurrent use. The report route limits reports with it; the
// sign-in, device login and client registration routes limit starts.
type Limiter struct {
	mu         sync.Mutex
	perClient  int
	overall    int
	window     time.Duration
	maxClients int
	clients    map[string]*counter
	global     counter
}

type counter struct {
	start time.Time
	n     int
}

// NewLimiter returns a limiter that allows perClient requests from one
// client and overall from all together in each window, tracking at most
// maxClients clients at once.
func NewLimiter(perClient, overall int, window time.Duration, maxClients int) *Limiter {
	return &Limiter{perClient: perClient, overall: overall, window: window, maxClients: maxClients, clients: map[string]*counter{}}
}

// Take reserves one request from key at now and reports whether it is
// allowed, and if not, how long until the window that refused it ends.
func (l *Limiter) Take(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.fresh(&l.global, now) && l.global.n >= l.overall {
		return false, l.global.start.Add(l.window).Sub(now)
	}
	c, ok := l.clients[key]
	if !ok {
		if len(l.clients) >= l.maxClients {
			l.prune(now)
		}
		if len(l.clients) >= l.maxClients {
			return false, l.window
		}
		c = &counter{start: now}
		l.clients[key] = c
	}
	if !l.fresh(c, now) && c.n >= l.perClient {
		return false, c.start.Add(l.window).Sub(now)
	}
	c.n++
	l.global.n++
	return true, 0
}

// fresh starts a new window for c when its window has ended and reports
// whether it did.
func (l *Limiter) fresh(c *counter, now time.Time) bool {
	if c.start.IsZero() || !now.Before(c.start.Add(l.window)) {
		c.start, c.n = now, 0
		return true
	}
	return false
}

// prune drops clients whose window has ended.
func (l *Limiter) prune(now time.Time) {
	for key, c := range l.clients {
		if !now.Before(c.start.Add(l.window)) {
			delete(l.clients, key)
		}
	}
}

// RetryAfter is the value of a Retry-After header for a wait of d: whole
// seconds, rounded up, and at least one.
func RetryAfter(d time.Duration) string {
	seconds := int64((d + time.Second - 1) / time.Second)
	return strconv.FormatInt(max(seconds, 1), 10)
}

// FloodSafeRate is the most starts a limiter with fixed windows may allow
// per window in front of a table of slots entries, full of one entry from
// each of many sources, where each start pushes out an entry chosen at
// random, so that an entry still survives the starts of one window's
// length after it with at least probability survival. Fixed windows let
// up to twice their rate through in one window's length, across the edge
// of two windows, so the bound is the largest n with
// (1 - 1/slots)^(2n) >= survival. For 1,024 slots and 0.85 it is 83, for
// 1,000 it is 81. It is at least 1.
func FloodSafeRate(slots int, survival float64) int {
	if slots <= 1 || survival <= 0 || survival >= 1 {
		return 1
	}
	return max(int(math.Log(survival)/(2*math.Log1p(-1/float64(slots)))), 1)
}

// Bounds of the start limits that sign-in, device login and client
// registration put in front of their tables of open entries.
const (
	// StartWindow is the window the start limits count in.
	StartWindow = time.Minute
	// StartsPerSource bounds the starts one network source may make in a
	// StartWindow; a person signing in, even retrying, makes a few.
	StartsPerSource = 10
	// StartSurvival is the least probability with which an entry, such as
	// a person's sign-in finished within a StartWindow, survives a flood
	// that fills its table with one entry from each of many sources.
	StartSurvival = 0.85
	// maxStartSources bounds the sources a start limiter tracks at once.
	maxStartSources = 10000
)

// NewStartLimiter returns the limiter of starts in front of a table of
// slots entries where a start pushes out an entry chosen at random once
// every source holds one: StartsPerSource from each source and, from all
// together, FloodSafeRate(slots, StartSurvival) per StartWindow, so that a
// flood rotating through any number of sources pushes out a given entry
// within a StartWindow with probability at most 1 - StartSurvival.
func NewStartLimiter(slots int) *Limiter {
	return NewLimiter(StartsPerSource, FloodSafeRate(slots, StartSurvival), StartWindow, maxStartSources)
}
