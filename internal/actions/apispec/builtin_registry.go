// Package apispec generates api/openapi.yaml and api/llms.txt from the specs
// of the registered actions, so the documents every surface and agent reads
// come from the same source as the action service itself.
package apispec

//go:generate go run ./genapi -dir ../../../api

import (
	"context"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/actions/approvalactions"
	"github.com/bestagentkits/sisf/internal/actions/pendingactions"
	"github.com/bestagentkits/sisf/internal/actions/runactions"
	"github.com/bestagentkits/sisf/pkg/core"
)

// BuiltinRegistry returns a registry of the specs of every built-in action,
// registered the way the integration package registers them: implemented
// sets with Register, pending ones with RegisterFallback. Its handlers only
// refuse; it exists to describe the actions, not to run them.
func BuiltinRegistry() (*actions.Registry, error) {
	reg := actions.NewRegistry()
	var implemented []core.Action
	for _, specs := range [][]core.ActionSpec{runactions.Specs(), approvalactions.Specs()} {
		for _, spec := range specs {
			implemented = append(implemented, describeOnly(spec))
		}
	}
	if err := reg.Register(implemented...); err != nil {
		return nil, err
	}
	if err := reg.RegisterFallback(pendingactions.Fallbacks()...); err != nil {
		return nil, err
	}
	return reg, nil
}

// describeOnly pairs a spec with a handler that refuses, for a registry
// that only describes actions.
func describeOnly(spec core.ActionSpec) core.Action {
	return core.Action{Spec: spec, Handle: func(context.Context, core.ActionRequest) (core.ActionResult, error) {
		return core.ActionResult{}, core.NewError(core.CodeNotSupported, "this registry describes actions and runs none")
	}}
}
