// Package apispec generates api/openapi.yaml and api/llms.txt from the specs
// of the registered actions, so the documents every surface and agent reads
// come from the same source as the action service itself.
package apispec

//go:generate go run ./genapi -dir ../../../api

import (
	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/actions/actionsets"
)

// BuiltinRegistry returns a registry of the specs of every built-in action,
// registered the way the control process registers them: the stand-ins
// first, then every real set over them. Its handlers only refuse; it exists
// to describe the actions, not to run them.
func BuiltinRegistry() (*actions.Registry, error) { return actionsets.Describe() }
