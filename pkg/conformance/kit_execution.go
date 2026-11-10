package conformance

import (
	"context"
	"fmt"

	"github.com/bestagentkits/sisf/internal/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

// ExecutionProbe is what the kit needs to exercise an execution backend.
type ExecutionProbe struct {
	// Spec describes a sandbox the backend is able to create.
	Spec execution.SandboxSpec
	// Command, when it has arguments, must run in that sandbox and exit 0.
	Command execution.Command
	// EndToEndProven records that the backend also passed an end-to-end run.
	EndToEndProven bool
}

// GradeExecution grades an execution backend. It starts from the backend's
// declaration and then takes one sandbox through its whole life: create,
// inspect, run a command, destroy, destroy again. Each step the backend
// fails lowers the area that step proves, whatever the backend declared.
// Isolation cannot be shown by a probe that behaves well, so it is graded
// from the declared isolation and egress-enforcement capabilities alone.
//
// An error means the backend could not be graded at all; a backend that was
// graded and found lacking is a Row, not an error.
func GradeExecution(ctx context.Context, backend execution.Backend, probe ExecutionProbe) (Row, error) {
	if backend == nil {
		return Row{}, core.NewError(core.CodeInvalidArgument, "no execution backend was given to grade")
	}
	if err := probe.Spec.Validate(); err != nil {
		return Row{}, err
	}
	declared, err := backend.Capabilities(ctx)
	if err != nil {
		return Row{}, undeclared(backend.Name(), err)
	}
	row := Grade(Declaration{Kind: KindExecution, Name: backend.Name(), Capabilities: declared, EndToEndProven: probe.EndToEndProven})

	if err := backend.Health(ctx); err != nil {
		row.lower(core.CapLifecycle, "the backend is not able to create sandboxes: "+err.Error())
		return row, nil
	}
	sandbox, err := backend.CreateSandbox(ctx, probe.Spec)
	if err != nil {
		row.lower(core.CapLifecycle, "a sandbox could not be created: "+err.Error())
		return row, nil
	}

	seen, err := backend.Inspect(ctx, sandbox.ID)
	switch {
	case err != nil:
		row.lower(core.CapObservability, "a running sandbox could not be inspected: "+err.Error())
	case seen.ID != sandbox.ID || seen.RunID != probe.Spec.RunID || seen.State != execution.SandboxRunning:
		row.lower(core.CapObservability, "inspection does not show the created sandbox running for its run")
	}

	if len(probe.Command.Argv) > 0 {
		result, err := backend.Exec(ctx, sandbox.ID, probe.Command)
		switch {
		case err != nil:
			row.lower(core.CapLifecycle, "a command could not be run in the sandbox: "+err.Error())
		case result.ExitCode != 0:
			row.lower(core.CapLifecycle, fmt.Sprintf("the probe command exited with status %d", result.ExitCode))
		}
	}

	// The way back from a bad run is to destroy its sandbox, completely and
	// as often as the caller asks.
	if err := backend.Destroy(ctx, sandbox.ID); err != nil {
		row.lower(core.CapRollback, "the sandbox could not be destroyed: "+err.Error())
		return row, nil
	}
	if err := backend.Destroy(ctx, sandbox.ID); err != nil {
		row.lower(core.CapRollback, "destroying a sandbox that is already gone must succeed: "+err.Error())
	}
	after, err := backend.Inspect(ctx, sandbox.ID)
	switch {
	case err == nil && after.State != execution.SandboxDestroyed:
		row.lower(core.CapRollback, fmt.Sprintf("the sandbox is still reported %s after it was destroyed", after.State))
	case err != nil && !core.HasCode(err, core.CodeNotFound):
		row.lower(core.CapObservability, "a destroyed sandbox must be reported destroyed or NOT_FOUND: "+err.Error())
	}
	return row, nil
}

// undeclared reports that a backend would not state its capabilities, so
// nothing about it can be relied on.
func undeclared(name string, cause error) error {
	return core.NewError(core.CodeUnavailable, fmt.Sprintf("backend %q did not declare its capabilities, so it cannot be graded", name)).WithCause(cause)
}
