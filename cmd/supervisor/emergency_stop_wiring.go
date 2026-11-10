package main

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/runcontrol"
	"github.com/bestagentkits/sisf/internal/audit"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/internal/supervisor/killswitch"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// enforceInterval is how often a stop in force is applied again, holding
// issues that arrived since and renewing the holds well within their TTL.
const enforceInterval = time.Minute

// emergencyStop is the kill switch a running supervisor serves, or nothing
// when the supervisor was not given the state store.
type emergencyStop struct {
	sw    *killswitch.Switch
	store *sqlstore.Store
	close func()
}

// openEmergencyStop opens the store named by the environment variable dsnEnv
// and builds the switch on it. Without the variable the supervisor serves
// status only; with it, a store that cannot be opened refuses the start, so
// a supervisor never looks able to stop the factory when it is not. With
// dispatcherURL the stop also cancels the agent runs and revokes their
// secrets in the dispatcher's process, through its run control route.
func openEmergencyStop(ctx context.Context, factory core.FactoryID, dsnEnv, dispatcherURL string, lookupEnv func(string) (string, bool), clock core.Clock) (*emergencyStop, error) {
	raw, ok := lookupEnv(dsnEnv)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	store, err := killswitch.OpenControlStore(ctx, core.NewSecretValue([]byte(raw)), factory, clock)
	if err != nil {
		return nil, err
	}
	cfg := killswitch.Config{FactoryID: factory, State: store, Clock: clock, Audit: audit.New(store, clock)}
	if dispatcherURL != "" {
		token, _ := lookupEnv(runcontrol.EnvToken)
		client, err := runcontrol.NewClient(dispatcherURL, core.NewSecretValue([]byte(token)))
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		cfg.Runs, cfg.Secrets = client, client
	}
	sw, err := killswitch.New(cfg)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	return &emergencyStop{sw: sw, store: store, close: func() { _ = store.Close() }}, nil
}

// options adds the switch's admin routes to opts.
func (e *emergencyStop) options(opts supervisor.Options) supervisor.Options {
	if e == nil {
		return opts
	}
	opts.EmergencyStop = e.sw.Stop
	opts.FreezeStatus = e.sw.Status
	return opts
}

// enforce applies a stop in force again every interval until ctx ends.
// Failures are reported on stderr; the next pass tries again.
func (e *emergencyStop) enforce(ctx context.Context, clock core.Clock, stderr io.Writer) {
	if e == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-clock.After(enforceInterval):
		}
		report, err := e.sw.Enforce(ctx)
		if err != nil {
			if ctx.Err() == nil {
				say(stderr, "%s: the emergency stop could not be enforced: %v\n", program, err)
			}
			continue
		}
		for _, failure := range report.Failures {
			say(stderr, "%s: emergency stop: %s\n", program, failure)
		}
	}
}
