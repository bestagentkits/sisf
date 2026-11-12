package oauthas

import (
	"time"

	"github.com/bestagentkits/sisf/internal/intake/report"
)

// LiftStartLimits lets clients register without the start limits, for a
// test of the table behind them: which client is dropped once the limits
// let registrations through.
func LiftStartLimits(cfg *Config) {
	cfg.registrationLimits = report.NewStartLimits(1<<30, 1<<30, time.Minute, 1<<20)
}
