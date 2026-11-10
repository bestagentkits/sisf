package report

import (
	"sync"
	"time"
)

// limiter counts requests in fixed windows, per client and overall. The
// client table is bounded: when it is full and nothing in it has expired, a
// new client is refused, so many addresses cannot exhaust memory.
type limiter struct {
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

// take reserves one request at now and reports whether it is allowed, and
// if not, how long until the window that refused it ends.
func (l *limiter) take(key string, now time.Time) (bool, time.Duration) {
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
func (l *limiter) fresh(c *counter, now time.Time) bool {
	if c.start.IsZero() || !now.Before(c.start.Add(l.window)) {
		c.start, c.n = now, 0
		return true
	}
	return false
}

// prune drops clients whose window has ended.
func (l *limiter) prune(now time.Time) {
	for key, c := range l.clients {
		if !now.Before(c.start.Add(l.window)) {
			delete(l.clients, key)
		}
	}
}
