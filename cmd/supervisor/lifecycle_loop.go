package main

import (
	"context"
	"log/slog"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/core"
)

// runStages runs a round of the stages every interval until ctx ends. A
// failure is logged, through the redacting logger, and retried next round.
func runStages(ctx context.Context, st *stages.Stages, l *lifecycleFlags, clock core.Clock, log *slog.Logger) {
	for {
		res := st.Pass(ctx)
		for _, err := range res.Errors() {
			if ctx.Err() == nil {
				log.Warn("a lifecycle stage failed", "code", string(core.CodeOf(err)), "error", err.Error())
			}
		}
		for _, issue := range res.Build.Submitted {
			log.Info("candidate built and submitted", "issue", issue.String())
		}
		for _, issue := range res.Build.Unbuildable {
			log.Info("patch refused by the builder; the run's next attempt follows", "issue", issue.String())
		}
		for _, issue := range res.Build.Held {
			log.Warn("candidate build held: it failed repeatedly on GitHub, the image builder or the configuration; no attempt was spent and it waits for the Owner's retry of the issue", "issue", issue.String())
		}
		select {
		case <-ctx.Done():
			return
		case <-clock.After(l.interval):
		}
	}
}

// startLifecycle builds the stages when the flags ask for them, adds the
// Owner rollback to opts and returns the loop to run; nil when the
// lifecycle is off.
func (l *lifecycleFlags) startLifecycle(ctx context.Context, d lifecycleDeps, opts *supervisor.Options) (func(context.Context), error) {
	if !l.enabled() {
		return nil, nil
	}
	st, err := l.buildLifecycle(ctx, d)
	if err != nil {
		return nil, err
	}
	opts.Rollback = st.Pipeline.OwnerRollback
	opts.HeldBuilds = heldBuilds(d.stop.store)
	return func(ctx context.Context) {
		if l.adopter != nil && !l.adopter.until(ctx, l, d.clock, d.log) {
			return
		}
		runStages(ctx, st, l, d.clock, d.log)
	}, nil
}

// getenv adapts lookupEnv to the getenv the process logger reads.
func getenv(lookupEnv func(string) (string, bool)) func(string) string {
	return func(name string) string {
		v, _ := lookupEnv(name)
		return v
	}
}
