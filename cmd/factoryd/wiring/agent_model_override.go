package wiring

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent"
	"github.com/bestagentkits/sisf/pkg/core"
)

// modelOverride runs every task of a coding agent on one model in place of
// the model its route names, keeping the provider and effort. The routes of
// a policy name tiers ("mid"), which a real tool does not accept; the
// operator maps the coding tier to a model with --agent-model.
type modelOverride struct {
	agent.Backend
	model string
}

// withModel wraps b so that every task runs on model.
func withModel(b agent.Backend, model string) (agent.Backend, error) {
	if err := cliagent.ValidateModel(model); err != nil {
		return nil, err
	}
	return modelOverride{Backend: b, model: model}, nil
}

// Start starts the task on the configured model.
func (m modelOverride) Start(ctx context.Context, task agent.Task, ws agent.WorkspaceRef, scopes []string) (agent.RunRef, error) {
	task.Policy.ModelRoute = withRouteModel(task.Policy.ModelRoute, m.model)
	return m.Backend.Start(ctx, task, ws, scopes)
}

// withRouteModel returns route with its model replaced.
func withRouteModel(route core.ModelRoute, model string) core.ModelRoute {
	route.Model = model
	return route
}
