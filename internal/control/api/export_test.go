package api

import (
	"net/http"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/report"
)

// NewWithEvictionPick is New with pick choosing which waiting login request
// to drop, so a test can seed the choice it otherwise makes with
// crypto/rand.
func NewWithEvictionPick(cfg Config, pick func(n int) (int, error)) (http.Handler, error) {
	return newHandler(cfg, pick)
}

// LiftStartLimits lets login requests start without the start limits, for
// a test of the table behind them: which request is dropped once the
// limits let starts through.
func LiftStartLimits(cfg *Config) {
	cfg.deviceStarts = report.NewStartLimits(1<<30, 1<<30, time.Minute, 1<<20)
}
