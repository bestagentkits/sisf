package auth

// SetEvictionPick makes s choose which sign-in to push out with pick, so a
// test can seed the choice the service otherwise makes with crypto/rand.
func SetEvictionPick(s *Service, pick func(n int) (int, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pick = pick
}
