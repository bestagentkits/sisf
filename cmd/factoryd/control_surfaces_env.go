package main

import (
	"io/fs"
	"os"
	"strings"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/cmd/supervisor/observe"
	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/internal/supervisor"
)

// surfacesFromEnv reads what the control role's surfaces need: sign-in, the
// supervisor's admin socket, the docs tree and the process's telemetry.
func surfacesFromEnv(getenv func(string) string, s zoneSetup, proc *observe.Process) (*wiring.SurfaceConfig, error) {
	identity, err := wiring.IdentityFromEnv(getenv, s.manifest, listenOr(s.env.Listen, wiring.DefaultControlListen))
	if err != nil {
		return nil, err
	}
	sup, err := supervisor.NewClient(listenOr(strings.TrimSpace(getenv(wiring.EnvSupervisorAdminAddr)), supervisor.DefaultAdminAddr))
	if err != nil {
		return nil, err
	}
	var docs fs.FS
	if dir := strings.TrimSpace(getenv(wiring.EnvDocsDir)); dir != "" {
		docs = os.DirFS(dir)
	}
	return &wiring.SurfaceConfig{Identity: identity, GitHubToken: s.env.GitHubToken, Supervisor: sup,
		Telemetry: proc.Telemetry, Docs: docs}, nil
}

// decisionsFromEnv turns the decision layer on when the edge's counters URL
// is set; the read-only counters token is then required. Without the URL
// nothing is decided. The client built here reads counts only: the control
// zone never holds a credential or a client that can change the split.
func decisionsFromEnv(getenv func(string) string) (*wiring.DecisionConfig, error) {
	base := strings.TrimSpace(getenv(wiring.EnvEdgeCountersURL))
	if base == "" {
		return nil, nil
	}
	counts, err := edge.NewCountsClient(base, strings.TrimSpace(getenv(wiring.EnvEdgeCountersToken)), nil)
	if err != nil {
		return nil, err
	}
	return &wiring.DecisionConfig{Counters: wiring.EdgeCounters{Edge: counts}}, nil
}
