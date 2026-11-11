package main

import (
	"log/slog"
	"path/filepath"
	"regexp"

	"github.com/bestagentkits/sisf/adapters/deploy/compose"
	"github.com/bestagentkits/sisf/adapters/deploy/local"
	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// localAddrEnv tells a locally started unit where to listen.
const localAddrEnv = "SISF_LISTEN"

// imageRefPattern is the shape of an image reference a local program is
// given as its first argument: no option, no shell syntax.
var imageRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+-]{0,254}$`)

// edgeAdmin connects to the edge's admin route. The token is read from the
// environment only and is never logged or put in an error.
func (l *lifecycleFlags) edgeAdmin(lookupEnv func(string) (string, bool)) (*edge.AdminClient, error) {
	if l.edgeAdminURL == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "releases need --edge-admin-url")
	}
	token, _ := lookupEnv(envEdgeAdminToken)
	if token == "" {
		return nil, core.NewError(core.CodeNotFound, envEdgeAdminToken+" is not set")
	}
	return edge.NewAdminClient(l.edgeAdminURL, token, nil)
}

// localSlotsRecord names, in the trust volume, the directory where the
// local deploy backend records the process of each slot, so a restarted
// supervisor finds again the application processes it started before.
const localSlotsRecord = "local-slots"

// deployBackend builds the configured deploy backend over the edge.
// trustDir is the trust volume, where the local backend records its
// processes; empty records nothing.
func (l *lifecycleFlags) deployBackend(factory core.FactoryID, ctl edge.Controller, trustDir string, clock core.Clock, log *slog.Logger) (deploy.Backend, error) {
	switch l.deploy {
	case "local":
		if l.localProgram == "" || !filepath.IsAbs(l.localProgram) {
			return nil, core.NewError(core.CodeInvalidArgument, "deploy local needs an absolute --deploy-local-program")
		}
		programs := l.programs
		if programs == nil {
			programs = &localPrograms{launcher: l.localProgram}
		}
		starter := local.ExecStarter{
			AddrEnv: localAddrEnv, Clock: clock, Command: programs.command,
			OnLog: func(line string) { log.Info("unit output", "line", line) },
		}
		var slots string
		if trustDir != "" {
			var err error
			if slots, err = supervisor.TrustPath(trustDir, localSlotsRecord); err != nil {
				return nil, err
			}
		}
		return local.New(local.Config{Factory: factory, Starter: starter, Edge: ctl, Clock: clock, Logger: log, StateDir: slots})
	case "compose":
		return compose.New(compose.Config{
			Endpoint: l.deployEndpoint, Factory: factory, Network: l.deployNetwork, AppPort: l.appPort,
			Edge: ctl, Clock: clock, Logger: log,
		})
	}
	return nil, core.NewError(core.CodeInvalidArgument, "--deploy must be local or compose")
}
