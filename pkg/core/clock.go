package core

import "time"

// Clock is the source of time for every component. Leases, expiries, hold
// windows and timeouts read it instead of the wall clock, so their behavior
// can be driven deterministically in tests.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
	// After delivers the instant once d has elapsed on this clock.
	After(d time.Duration) <-chan time.Time
}

// SystemClock is the wall clock. Instants are reported in UTC so stored and
// signed timestamps do not depend on the host's zone; presenting them in a
// local zone is a display concern.
type SystemClock struct{}

// Now returns the current wall-clock instant in UTC.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// After waits on the wall clock.
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
