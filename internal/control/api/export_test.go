package api

import "net/http"

// NewWithEvictionPick is New with pick choosing which waiting login request
// to drop, so a test can seed the choice it otherwise makes with
// crypto/rand.
func NewWithEvictionPick(cfg Config, pick func(n int) (int, error)) (http.Handler, error) {
	return newHandler(cfg, pick)
}
