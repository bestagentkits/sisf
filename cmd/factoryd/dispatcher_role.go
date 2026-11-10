package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/bestagentkits/sisf/cmd/factoryd/lifecycle"
	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/cmd/supervisor/runcontrol"
	"github.com/bestagentkits/sisf/pkg/core"
)

// dispatcherFlags are the agent zone's choices; secrets come from the
// environment only.
type dispatcherFlags struct {
	work                                        time.Duration
	agent, patchFile, adminListen               string
	workspaceRoot, workspaceSource, artifactDir string
	sandboxed                                   bool
	// estimate is what each run sets aside from a capped budget.
	estimate int64
}

func (d *dispatcherFlags) register(fs *flag.FlagSet) {
	fs.DurationVar(&d.work, "fake-agent-work", defaultFakeAgentWork, "how long a scripted agent run works before it leaves its patch")
	fs.StringVar(&d.agent, "agent", "fake", "coding agent: fake, claude, codex or pi")
	fs.StringVar(&d.patchFile, "fake-agent-patch", "", "unified diff the scripted agent leaves on every run")
	fs.StringVar(&d.workspaceRoot, "workspace-root", "", "absolute directory of the fresh per-attempt workspaces of a real agent")
	fs.StringVar(&d.workspaceSource, "workspace-source", "", "repository URL or path, without credentials, workspaces are checked out from")
	fs.StringVar(&d.artifactDir, "artifact-dir", "", "absolute directory real agents write their artifacts to")
	fs.BoolVar(&d.sandboxed, "sandboxed", false, "declare that this process runs inside a sandbox (required for pi)")
	fs.Int64Var(&d.estimate, "run-estimate-micros", 0, "amount each run sets aside from a capped budget, in micro-units; a capped budget needs it")
	fs.StringVar(&d.adminListen, "admin-listen", "", "address of the run control route the supervisor's emergency stop calls; needs "+runcontrol.EnvToken)
}

// runDispatcher runs the dispatcher, the patch exporter, the health
// endpoint and, when configured, the run control route until ctx ends.
func runDispatcher(ctx context.Context, args []string, getenv func(string) string, log *slog.Logger) error {
	var df dispatcherFlags
	fs := newFlags("dispatcher")
	df.register(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	if df.work < 0 {
		return fmt.Errorf("%w: --fake-agent-work must not be negative", errUsage)
	}
	if df.estimate < 0 {
		return fmt.Errorf("%w: --run-estimate-micros must not be negative", errUsage)
	}
	s, err := setup(ctx, getenv, core.ZoneAgent)
	if err != nil {
		return err
	}
	defer func() { _ = s.store.Close() }()
	zone, err := newAgentZone(s, df)
	if err != nil {
		return err
	}
	d, err := wiring.NewDispatcher(wiring.DispatcherConfig{
		Manifest: s.manifest, Store: s.store, Agent: zone.Agent, Clock: s.clock,
		PollInterval: wiring.DispatcherPollInterval, LeaseTTL: wiring.DispatcherLeaseTTL, Estimate: core.Micros(df.estimate),
	})
	if err != nil {
		return err
	}
	ln, err := wiring.Listen(ctx, listenOr(s.env.Listen, wiring.DefaultDispatcherListen), s.env.AllowNonLoopback)
	if err != nil {
		return err
	}
	log.Info("dispatcher running", "factory", string(s.manifest.FactoryID), "owner", d.Health().Owner, "agent", df.agent, "health", ln.Addr().String())
	errs := make(chan error, 3)
	go func() {
		errs <- wiring.Serve(ctx, ln, wiring.DispatcherHealth(d, s.clock, 3*wiring.DispatcherLeaseTTL))
	}()
	go func() { errs <- exportLoop(ctx, zone.Exporter, s.clock, log) }()
	routes := 2
	if df.adminListen != "" {
		handler, err := runcontrol.Handler(getenv(runcontrol.EnvToken), zone.Agent, zone.Broker)
		if err != nil {
			return err
		}
		admin, err := wiring.Listen(ctx, df.adminListen, s.env.AllowNonLoopback)
		if err != nil {
			return err
		}
		routes++
		go func() { errs <- wiring.Serve(ctx, admin, handler) }()
	}
	runErr := d.Run(ctx)
	for range routes {
		if err := <-errs; err != nil && runErr == nil {
			runErr = err
		}
	}
	return runErr
}

// newAgentZone builds the coding agent and the patch exporter. The agent
// zone has no GitHub client: it never holds the repository's credentials.
func newAgentZone(s zoneSetup, df dispatcherFlags) (*wiring.AgentZone, error) {
	patch := ""
	if df.patchFile != "" {
		raw, err := os.ReadFile(df.patchFile)
		if err != nil {
			return nil, core.NewError(core.CodeInvalidArgument, "the scripted agent's patch cannot be read").WithCause(err)
		}
		patch = string(raw)
	}
	return wiring.NewAgentZone(wiring.AgentZoneConfig{
		Store: s.store, Clock: s.clock, Agent: df.agent, FakeWork: df.work, FakePatch: patch,
		WorkspaceRoot: df.workspaceRoot, WorkspaceSource: df.workspaceSource, ArtifactDir: df.artifactDir,
		Sandboxed: df.sandboxed, Owner: "exporter@" + wiring.DispatcherOwner(),
	})
}

// exportLoop runs the exporter every poll interval until ctx ends.
func exportLoop(ctx context.Context, e *lifecycle.Exporter, clock core.Clock, log *slog.Logger) error {
	for {
		res := e.Pass(ctx)
		for _, err := range res.Errors {
			log.Warn("a patch could not be exported", "code", string(core.CodeOf(err)))
		}
		for _, issue := range res.Exported {
			log.Info("patch exported", "issue", issue.String())
		}
		for _, issue := range res.Retried {
			log.Info("run sent back to its coding agent", "issue", issue.String())
		}
		select {
		case <-ctx.Done():
			return nil
		case <-clock.After(wiring.DispatcherPollInterval):
		}
	}
}
