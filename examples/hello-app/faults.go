package main

import (
	"errors"
	"fmt"
	"strconv"
)

// Environment variables that set the revision and inject faults, so a
// release pipeline can prove its monitor and rollback against a real app.
const (
	envRevision    = "SISF_REVISION"
	envErrorPct    = "HELLO_FAULT_ERROR_PERCENT"
	envUnhealthy   = "HELLO_FAULT_UNHEALTHY"
	envCriticalLog = "HELLO_FAULT_CRITICAL_LOG"
)

// settings is what the environment configures.
type settings struct {
	// Revision is what /healthz reports as the running revision.
	Revision string
	// ErrorPercent of page requests fail with 500, evenly spread: with 25,
	// every fourth request fails.
	ErrorPercent int
	// Unhealthy makes /healthz answer 503.
	Unhealthy bool
	// CriticalLog writes a critical-level line for each injected failure.
	CriticalLog bool
}

// settingsFromEnv reads and validates the settings. A malformed value stops
// the app from starting: a fault that silently does not apply would let a
// test pass for the wrong reason.
func settingsFromEnv(getenv func(string) string) (settings, error) {
	s := settings{Revision: getenv(envRevision)}
	if len(s.Revision) > 200 {
		return s, errors.New(envRevision + " is longer than 200 characters")
	}
	if raw := getenv(envErrorPct); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 100 {
			return s, fmt.Errorf("%s must be a whole number from 0 to 100", envErrorPct)
		}
		s.ErrorPercent = n
	}
	for name, dst := range map[string]*bool{envUnhealthy: &s.Unhealthy, envCriticalLog: &s.CriticalLog} {
		if raw := getenv(name); raw != "" {
			b, err := strconv.ParseBool(raw)
			if err != nil {
				return s, fmt.Errorf("%s must be true or false", name)
			}
			*dst = b
		}
	}
	return s, nil
}

// injectedError reports whether this page request is one that must fail,
// and logs it when asked to.
func (a *app) injectedError() bool {
	if a.cfg.ErrorPercent == 0 {
		return false
	}
	a.mu.Lock()
	a.faultAcc += a.cfg.ErrorPercent
	fail := a.faultAcc >= 100
	if fail {
		a.faultAcc -= 100
	}
	a.mu.Unlock()
	if fail && a.cfg.CriticalLog {
		_, _ = fmt.Fprintf(a.logs, "level=critical msg=%q revision=%q\n", "injected failure", a.cfg.Revision) // a broken log pipe must not change the fault
	}
	return fail
}
