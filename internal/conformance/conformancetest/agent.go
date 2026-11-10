package conformancetest

import (
	"context"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/internal/core"
	"github.com/bestagentkits/sisf/pkg/agent"
)

// Agent is an in-memory agent backend. A started run stays running until it
// is cancelled.
type Agent struct {
	BackendName string
	Declared    core.CapabilitySet
	// Fail makes the named method ("Capabilities", "Start", "Status",
	// "Events", "Cancel", "CollectArtifacts") return the given error.
	Fail map[string]error
	// CancelDoesNothing makes Cancel report success while the run goes on.
	CancelDoesNothing bool

	mu   sync.Mutex
	runs map[string]agent.Phase
}

var _ agent.Backend = (*Agent)(nil)

// Name returns the backend's name.
func (a *Agent) Name() string { return a.BackendName }

// Capabilities returns the declared set.
func (a *Agent) Capabilities(context.Context) (core.CapabilitySet, error) {
	return a.Declared, a.Fail["Capabilities"]
}

// Start records a running run for the task.
func (a *Agent) Start(_ context.Context, task agent.Task, _ agent.WorkspaceRef, _ []string) (agent.RunRef, error) {
	if err := a.Fail["Start"]; err != nil {
		return agent.RunRef{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runs == nil {
		a.runs = map[string]agent.Phase{}
	}
	id := "session-" + string(task.RunID)
	a.runs[id] = agent.PhaseRunning
	return agent.RunRef{Backend: a.BackendName, ID: id, RunID: task.RunID}, nil
}

// Status reports the phase of the run.
func (a *Agent) Status(_ context.Context, run agent.RunRef) (agent.Status, error) {
	if err := a.Fail["Status"]; err != nil {
		return agent.Status{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	phase, ok := a.runs[run.ID]
	if !ok {
		return agent.Status{}, core.NewError(core.CodeNotFound, "no such run")
	}
	return agent.Status{Phase: phase, UpdatedAt: time.Now()}, nil
}

// Events returns one status event.
func (a *Agent) Events(_ context.Context, _ agent.RunRef, _ agent.Cursor) (agent.EventPage, error) {
	if err := a.Fail["Events"]; err != nil {
		return agent.EventPage{}, err
	}
	return agent.EventPage{Events: []agent.Event{{Cursor: "1", At: time.Now(), Kind: agent.EventStatus}}, Next: "1"}, nil
}

// Cancel stops the run, or refuses honestly when cancelling is not declared.
func (a *Agent) Cancel(_ context.Context, run agent.RunRef) error {
	if err := a.Fail["Cancel"]; err != nil {
		return err
	}
	if a.Declared.Level(core.CapAgentCancel) == core.SupportNotSupported {
		return core.NewError(core.CodeNotSupported, "this backend cannot cancel a run")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.CancelDoesNothing {
		a.runs[run.ID] = agent.PhaseCancelled
	}
	return nil
}

// CollectArtifacts returns no artifacts.
func (a *Agent) CollectArtifacts(context.Context, agent.RunRef) ([]agent.Artifact, error) {
	return nil, a.Fail["CollectArtifacts"]
}
