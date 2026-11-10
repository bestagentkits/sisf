package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
)

// FreshWorkspaces gives every coding attempt a workspace of its own, made
// new when the attempt starts. A dispatcher that died can leave its agent
// process behind, still writing to the workspace it was given; the attempt
// that resumes the run after it never shares that workspace, and every
// earlier workspace of the run is removed before the new one is made, so
// what the orphan writes lands nowhere any later attempt reads.
//
// A Start repeated with the same idempotency key by the same process gets
// the workspace it got the first time, as the backend returns the same run.
type FreshWorkspaces struct {
	agent.Backend
	// Root holds the workspaces, one directory per run.
	Root string
	// Provision fills a new workspace with the checkout of base; nil leaves
	// it empty.
	Provision func(ctx context.Context, dir string, base core.CommitSHA) error

	mu    sync.Mutex
	byKey map[string]string
}

// Start makes the attempt's workspace and starts the run in it.
func (f *FreshWorkspaces) Start(ctx context.Context, task agent.Task, ws agent.WorkspaceRef, scopes []string) (agent.RunRef, error) {
	if err := task.RunID.Validate(); err != nil {
		return agent.RunRef{}, err
	}
	f.mu.Lock()
	path, ok := f.byKey[task.IdempotencyKey]
	f.mu.Unlock()
	if !ok {
		var err error
		if path, err = f.fresh(ctx, task.RunID, ws.BaseSHA); err != nil {
			return agent.RunRef{}, err
		}
		f.mu.Lock()
		if f.byKey == nil {
			f.byKey = map[string]string{}
		}
		f.byKey[task.IdempotencyKey] = path
		f.mu.Unlock()
	}
	ws.Path = path
	return f.Backend.Start(ctx, task, ws, scopes)
}

// fresh removes the earlier workspaces of run and makes a new one.
func (f *FreshWorkspaces) fresh(ctx context.Context, run core.RunID, base core.CommitSHA) (string, error) {
	if f.Root == "" || !filepath.IsAbs(f.Root) {
		return "", core.NewError(core.CodeInvalidArgument, "the workspace root must be an absolute path")
	}
	runDir := filepath.Join(f.Root, filepath.Base(string(run)))
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return "", core.NewError(core.CodeUnavailable, "the workspace directory cannot be made").WithCause(err)
	}
	earlier, err := os.ReadDir(runDir)
	if err != nil {
		return "", core.NewError(core.CodeUnavailable, "the workspace directory cannot be read").WithCause(err)
	}
	for _, entry := range earlier {
		if err := os.RemoveAll(filepath.Join(runDir, entry.Name())); err != nil {
			return "", core.NewError(core.CodeUnavailable, "an earlier workspace of the run cannot be removed").WithCause(err)
		}
	}
	dir, err := os.MkdirTemp(runDir, "attempt-")
	if err != nil {
		return "", core.NewError(core.CodeUnavailable, "the workspace cannot be made").WithCause(err)
	}
	if f.Provision != nil {
		if err := f.Provision(ctx, dir, base); err != nil {
			_ = os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

// SandboxedAgent refuses an agent backend that has no permission system of
// its own, such as Pi, unless its workspaces are fresh per attempt and the
// process runs inside a sandbox the operator declared.
func SandboxedAgent(name string, sandboxed bool, root string) error {
	if name != "pi" {
		return nil
	}
	if !sandboxed || root == "" {
		return core.NewError(core.CodePermissionDenied,
			"the pi agent runs with the rights of its process: it needs a workspace root and a sandboxed dispatcher")
	}
	return nil
}
