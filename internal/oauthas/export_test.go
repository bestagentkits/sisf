package oauthas

// SetEvictionPick makes s choose which registered client to drop with
// pick, so a test can seed the choice it otherwise makes with crypto/rand.
func SetEvictionPick(s *Server, pick func(n int) (int, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pick = pick
}
