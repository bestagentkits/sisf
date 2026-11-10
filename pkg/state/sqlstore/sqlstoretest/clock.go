package sqlstoretest

import (
	"sync"
	"time"
)

// Clock is a clock that moves only when a test moves it, so lease expiry,
// retry schedules and timestamps are exact and no test has to sleep.
type Clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []timer
}

type timer struct {
	due time.Time
	ch  chan time.Time
}

// NewClock returns a clock standing at start.
func NewClock(start time.Time) *Clock { return &Clock{now: start} }

// Now returns the instant the clock stands at.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After delivers the instant once the clock has been advanced by d.
func (c *Clock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.timers = append(c.timers, timer{due: c.now.Add(d), ch: ch})
	return ch
}

// Advance moves the clock forward by d and fires every timer that came due.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	waiting := c.timers[:0]
	for _, t := range c.timers {
		if t.due.After(c.now) {
			waiting = append(waiting, t)
			continue
		}
		t.ch <- c.now
	}
	c.timers = waiting
}
