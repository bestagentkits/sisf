package agent

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// RunInfo is what a backend knows about how a run was executed, for the run
// record: which tool ran, at which version, on which model route and under
// which scopes. It holds no credentials.
type RunInfo struct {
	Backend string `json:"backend"`
	// Tool is the program that did the work, for example "claude".
	Tool string `json:"tool"`
	// ToolVersion is the version the tool reported when the run started.
	ToolVersion string          `json:"tool_version"`
	ModelRoute  core.ModelRoute `json:"model_route"`
	Scopes      []string        `json:"scopes,omitempty"`
	StartedAt   time.Time       `json:"started_at"`
}

// RunInfoProvider is implemented by backends that wrap a local tool and can
// say which version of it ran.
type RunInfoProvider interface {
	RunInfo(ctx context.Context, run RunRef) (RunInfo, error)
}
