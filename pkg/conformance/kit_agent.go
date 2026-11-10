package conformance

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
)

// AgentProbe is what the kit needs to exercise an agent backend.
type AgentProbe struct {
	// Task is a small task the backend is able to start.
	Task      agent.Task
	Workspace agent.WorkspaceRef
	Scopes    []string
	// EndToEndProven records that the backend also passed an end-to-end run.
	EndToEndProven bool
}

// GradeAgent grades an agent backend. It starts from the backend's
// declaration and then starts one run, reads its status and events, cancels
// it and collects what it left behind. Each step the backend fails lowers
// the area that step proves. A backend that declares it cannot cancel must
// refuse with NOT_SUPPORTED; pretending to cancel is graded as a failure.
// Isolation is graded from the declaration alone.
//
// An error means the backend could not be graded at all.
func GradeAgent(ctx context.Context, backend agent.Backend, probe AgentProbe) (Row, error) {
	if backend == nil {
		return Row{}, core.NewError(core.CodeInvalidArgument, "no agent backend was given to grade")
	}
	if err := probe.Task.Validate(); err != nil {
		return Row{}, err
	}
	declared, err := backend.Capabilities(ctx)
	if err != nil {
		return Row{}, undeclared(backend.Name(), err)
	}
	row := Grade(Declaration{Kind: KindAgent, Name: backend.Name(), Capabilities: declared, EndToEndProven: probe.EndToEndProven})

	run, err := backend.Start(ctx, probe.Task, probe.Workspace, probe.Scopes)
	switch {
	case err != nil:
		row.lower(core.CapLifecycle, "the run could not be started: "+err.Error())
		return row, nil
	case run.RunID != probe.Task.RunID:
		row.lower(core.CapLifecycle, "the run handle does not carry the factory's run identifier")
	}

	if status, err := backend.Status(ctx, run); err != nil {
		row.lower(core.CapObservability, "the status of a started run could not be read: "+err.Error())
	} else if !status.Phase.Valid() {
		row.lower(core.CapObservability, "the run reports a phase the factory does not know")
	}
	page, err := backend.Events(ctx, run, "")
	if err != nil {
		row.lower(core.CapObservability, "the events of a started run could not be read: "+err.Error())
	}
	for _, event := range page.Events {
		if !event.Kind.Valid() {
			row.lower(core.CapObservability, "the event stream holds an event of an unknown kind")
			break
		}
	}

	cancelErr := backend.Cancel(ctx, run)
	if declared.Level(core.CapAgentCancel) == core.SupportNotSupported {
		if !core.HasCode(cancelErr, core.CodeNotSupported) {
			row.lower(core.CapLifecycle, "a backend that cannot cancel must refuse with NOT_SUPPORTED, not pretend")
		}
	} else if cancelErr != nil {
		row.lower(core.CapRollback, "the run could not be cancelled: "+cancelErr.Error())
	} else if status, err := backend.Status(ctx, run); err != nil || !status.Phase.Terminal() {
		row.lower(core.CapRollback, "the run was cancelled but is not reported finished")
	}

	if _, err := backend.CollectArtifacts(ctx, run); err != nil {
		row.lower(core.CapLifecycle, "the artifacts of the run could not be collected: "+err.Error())
	}
	return row, nil
}
