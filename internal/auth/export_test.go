package auth

import (
	"time"

	"github.com/bestagentkits/sisf/internal/intake/report"
)

// LiftStartLimits lets s start sign-ins and step-ups without the start
// limits, for a test of the tables behind them: which flow or session is
// pushed out once the limits let starts through.
func LiftStartLimits(s *Service) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts = report.NewLimiter(1<<30, 1<<30, time.Minute, 1<<20)
}

// SetEvictionPick makes s choose which sign-in to push out with pick, so a
// test can seed the choice the service otherwise makes with crypto/rand.
func SetEvictionPick(s *Service, pick func(n int) (int, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pick = pick
}
