// Package conformancetest holds in-memory backends for tests of the
// conformance kit and the capability matrix. Each backend behaves correctly
// by default and can be bent, one fault at a time, to show that the kit
// notices. Nothing here runs code, deploys anything or belongs in a factory.
package conformancetest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

// Execution is an in-memory execution backend.
type Execution struct {
	BackendName string
	Declared    core.CapabilitySet
	// Fail makes the named method ("Capabilities", "Health", "CreateSandbox",
	// "Inspect", "Exec", "Destroy") return the given error.
	Fail map[string]error
	// ExitCode is what every command exits with.
	ExitCode int
	// LeaveBehind makes Destroy report success while the sandbox keeps running.
	LeaveBehind bool
	// DestroyOnce makes a second Destroy of the same sandbox fail.
	DestroyOnce bool

	mu        sync.Mutex
	sandboxes map[execution.SandboxID]execution.SandboxInfo
}

var _ execution.Backend = (*Execution)(nil)

// Name returns the backend's name.
func (e *Execution) Name() string { return e.BackendName }

// Capabilities returns the declared set.
func (e *Execution) Capabilities(context.Context) (core.CapabilitySet, error) {
	return e.Declared, e.Fail["Capabilities"]
}

// Health reports whether sandboxes can be created.
func (e *Execution) Health(context.Context) error { return e.Fail["Health"] }

// CreateSandbox records a running sandbox for the run.
func (e *Execution) CreateSandbox(_ context.Context, spec execution.SandboxSpec) (execution.SandboxInfo, error) {
	if err := e.Fail["CreateSandbox"]; err != nil {
		return execution.SandboxInfo{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sandboxes == nil {
		e.sandboxes = map[execution.SandboxID]execution.SandboxInfo{}
	}
	info := execution.SandboxInfo{
		ID:    execution.SandboxID(fmt.Sprintf("sandbox-%d", len(e.sandboxes)+1)),
		RunID: spec.RunID, State: execution.SandboxRunning, Trust: spec.Trust, Image: spec.Image, CreatedAt: time.Now(),
	}
	e.sandboxes[info.ID] = info
	return info, nil
}

// Exec pretends to run the command and reports ExitCode.
func (e *Execution) Exec(_ context.Context, id execution.SandboxID, _ execution.Command) (execution.ExecResult, error) {
	if err := e.Fail["Exec"]; err != nil {
		return execution.ExecResult{}, err
	}
	if _, err := e.Inspect(context.Background(), id); err != nil {
		return execution.ExecResult{}, err
	}
	return execution.ExecResult{ExitCode: e.ExitCode}, nil
}

// Files is not part of what the kit exercises.
func (e *Execution) Files(context.Context, execution.SandboxID) (execution.FileSystem, error) {
	return nil, core.NewError(core.CodeNotSupported, "this backend has no file access")
}

// Snapshot is not part of what the kit exercises.
func (e *Execution) Snapshot(context.Context, execution.SandboxID) (execution.SnapshotRef, error) {
	return execution.SnapshotRef{}, core.NewError(core.CodeNotSupported, "this backend takes no snapshots")
}

// Inspect returns what is recorded about the sandbox.
func (e *Execution) Inspect(_ context.Context, id execution.SandboxID) (execution.SandboxInfo, error) {
	if err := e.Fail["Inspect"]; err != nil {
		return execution.SandboxInfo{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	info, ok := e.sandboxes[id]
	if !ok {
		return execution.SandboxInfo{}, core.NewError(core.CodeNotFound, "no such sandbox")
	}
	return info, nil
}

// Destroy marks the sandbox destroyed; destroying it again succeeds.
func (e *Execution) Destroy(_ context.Context, id execution.SandboxID) error {
	if err := e.Fail["Destroy"]; err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	info, ok := e.sandboxes[id]
	if !ok || e.LeaveBehind {
		return nil
	}
	if info.State == execution.SandboxDestroyed && e.DestroyOnce {
		return core.NewError(core.CodeNotFound, "the sandbox is already gone")
	}
	info.State = execution.SandboxDestroyed
	e.sandboxes[id] = info
	return nil
}
